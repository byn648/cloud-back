package schedule

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/ledongthuc/pdf"
)

const (
	officialElectricityPriceDownloadLimit = 16 << 20
	beijingOfficialPricePDFURL            = "https://www.95598.cn/omg-static/omg-static/99306272029057823884400238783751.pdf"
)

type officialElectricityPriceReading struct {
	price     float64
	ok        bool
	region    string
	sourceURL string
	period    string
	err       error
}

type electricityPriceTable struct {
	title          string
	sourceURL      string
	effectiveStart time.Time
	effectiveEnd   time.Time
	rows           []electricityPriceRow
}

type electricityPriceRow struct {
	billingMode              string
	voltageGrade             string
	nonTimePrice             float64
	hasSharpPrice            bool
	sharpPrice               float64
	peakPrice                float64
	flatPrice                float64
	valleyPrice              float64
	maxDemandPrice           float64
	transformerCapacityPrice float64
}

type cachedElectricityPriceTable struct {
	table     *electricityPriceTable
	expiresAt time.Time
}

var (
	electricityPriceCacheMu sync.Mutex
	electricityPriceCache   = map[string]cachedElectricityPriceTable{}
)

func (l *BuildRlSchedulePlanLogic) loadOfficialElectricityPrice(region string, clusterName string) officialElectricityPriceReading {
	key, sourceURL, ok := officialElectricityPriceSource(region, clusterName)
	if !ok {
		return officialElectricityPriceReading{
			region: firstNonEmpty(strings.TrimSpace(region), strings.TrimSpace(clusterName)),
			err:    fmt.Errorf("unsupported electricity price region"),
		}
	}

	table, err := l.loadOfficialElectricityPriceTable(key, sourceURL)
	if err != nil {
		return officialElectricityPriceReading{region: key, sourceURL: sourceURL, err: err}
	}

	now := time.Now()
	if loc, err := time.LoadLocation("Asia/Shanghai"); err == nil {
		now = now.In(loc)
	}
	if !electricityPriceTableEffective(table, now) {
		return officialElectricityPriceReading{
			region:    key,
			sourceURL: sourceURL,
			err:       fmt.Errorf("official electricity price table is not effective for current date"),
		}
	}

	row, ok := selectDefaultElectricityPriceRow(table.rows)
	if !ok {
		return officialElectricityPriceReading{region: key, sourceURL: sourceURL, err: fmt.Errorf("no usable electricity price row found")}
	}
	period := electricityPricePeriodAt(now)
	price, ok := electricityPriceForPeriod(row, period)
	if !ok {
		return officialElectricityPriceReading{region: key, sourceURL: sourceURL, err: fmt.Errorf("no electricity price for current period")}
	}

	return officialElectricityPriceReading{
		price:     price,
		ok:        true,
		region:    key,
		sourceURL: sourceURL,
		period:    period,
	}
}

func (l *BuildRlSchedulePlanLogic) loadOfficialElectricityPriceTable(cacheKey string, sourceURL string) (*electricityPriceTable, error) {
	now := time.Now()
	electricityPriceCacheMu.Lock()
	if cached, ok := electricityPriceCache[cacheKey]; ok && now.Before(cached.expiresAt) {
		table := cached.table
		electricityPriceCacheMu.Unlock()
		return table, nil
	}
	electricityPriceCacheMu.Unlock()

	data, contentType, err := downloadOfficialElectricityPriceFile(l.ctx, sourceURL)
	if err != nil {
		return nil, err
	}
	if !looksLikeOfficialPDF(sourceURL, contentType, data) {
		return nil, fmt.Errorf("official electricity price source is not a PDF file")
	}
	text, err := extractOfficialPDFText(data)
	if err != nil {
		return nil, fmt.Errorf("extract official electricity price PDF failed: %w", err)
	}
	table, err := parseBeijingAgentPurchasePriceTable(text)
	if err != nil {
		return nil, err
	}
	table.sourceURL = sourceURL

	electricityPriceCacheMu.Lock()
	electricityPriceCache[cacheKey] = cachedElectricityPriceTable{
		table:     table,
		expiresAt: now.Add(time.Hour),
	}
	electricityPriceCacheMu.Unlock()
	return table, nil
}

func officialElectricityPriceSource(region string, clusterName string) (string, string, bool) {
	text := strings.ToLower(strings.TrimSpace(region + " " + clusterName))
	if strings.Contains(text, "beijing") ||
		strings.Contains(text, "cn-beijing") ||
		strings.Contains(text, "cn-north-1") ||
		strings.Contains(text, "北京") {
		return "beijing", beijingOfficialPricePDFURL, true
	}
	return "", "", false
}

func downloadOfficialElectricityPriceFile(ctx context.Context, sourceURL string) ([]byte, string, error) {
	parsed, err := url.ParseRequestURI(sourceURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, "", fmt.Errorf("invalid official electricity price URL")
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return nil, "", fmt.Errorf("unsupported official electricity price URL scheme")
	}

	requestCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, sourceURL, nil)
	if err != nil {
		return nil, "", err
	}
	request.Header.Set("User-Agent", "Mozilla/5.0 kube-nova official-electricity-price")
	request.Header.Set("Accept", "application/pdf,application/octet-stream,*/*;q=0.5")

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, "", fmt.Errorf("download official electricity price file failed: %w", err)
	}
	defer response.Body.Close()

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, "", fmt.Errorf("download official electricity price file failed: HTTP %d", response.StatusCode)
	}

	limited := io.LimitReader(response.Body, officialElectricityPriceDownloadLimit+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, "", fmt.Errorf("read official electricity price file failed: %w", err)
	}
	if len(data) > officialElectricityPriceDownloadLimit {
		return nil, "", fmt.Errorf("official electricity price file is too large")
	}
	return data, response.Header.Get("Content-Type"), nil
}

func looksLikeOfficialPDF(sourceURL string, contentType string, data []byte) bool {
	if strings.Contains(strings.ToLower(contentType), "pdf") {
		return true
	}
	if strings.HasSuffix(strings.ToLower(strings.Split(sourceURL, "?")[0]), ".pdf") {
		return true
	}
	return bytes.HasPrefix(bytes.TrimSpace(data), []byte("%PDF"))
}

func extractOfficialPDFText(data []byte) (string, error) {
	tmp, err := os.CreateTemp("", "kube-nova-official-electricity-price-*.pdf")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(data); err != nil {
		tmp.Close()
		return "", err
	}
	if err = tmp.Close(); err != nil {
		return "", err
	}

	file, reader, err := pdf.Open(tmp.Name())
	if err != nil {
		return "", err
	}
	defer file.Close()

	plain, err := reader.GetPlainText()
	if err != nil {
		return "", err
	}
	text, err := io.ReadAll(plain)
	if err != nil {
		return "", err
	}
	return string(text), nil
}

func parseBeijingAgentPurchasePriceTable(text string) (*electricityPriceTable, error) {
	compact := compactElectricityPriceText(text)
	if !strings.Contains(compact, "代理购电工商业用户电价表") {
		return nil, fmt.Errorf("official PDF is not an agent-purchase industrial electricity price table")
	}

	start, end := parseElectricityPriceEffectiveDates(compact)
	if start.IsZero() || end.IsZero() {
		return nil, fmt.Errorf("official electricity price effective dates were not found")
	}

	firstTable := text
	if idx := strings.Index(compact, "国网北京市电力公司执行1.5倍代理购电价格工商业用户电价表"); idx >= 0 {
		firstTable = sliceByCompactElectricityPriceIndex(text, idx)
	}
	if idx := strings.Index(firstTable, "注"); idx >= 0 {
		firstTable = firstTable[:idx]
	}

	rows := parseElectricityPriceRows(firstTable, start)
	if len(rows) == 0 {
		return nil, fmt.Errorf("no electricity price rows were parsed from official PDF")
	}

	return &electricityPriceTable{
		title:          "State Grid Beijing agent-purchase industrial electricity price table",
		effectiveStart: start,
		effectiveEnd:   end,
		rows:           rows,
	}, nil
}

func parseElectricityPriceEffectiveDates(compact string) (time.Time, time.Time) {
	re := regexp.MustCompile(`执行时间[:：](\d{4})年(\d{1,2})月(\d{1,2})日[-－](\d{4})年(\d{1,2})月(\d{1,2})日`)
	match := re.FindStringSubmatch(compact)
	if len(match) != 7 {
		return time.Time{}, time.Time{}
	}
	return parseYMD(match[1], match[2], match[3]), parseYMD(match[4], match[5], match[6])
}

func parseYMD(year string, month string, day string) time.Time {
	yi, _ := strconv.Atoi(year)
	mi, _ := strconv.Atoi(month)
	di, _ := strconv.Atoi(day)
	return time.Date(yi, time.Month(mi), di, 0, 0, 0, 0, time.Local)
}

func electricityPriceTableEffective(table *electricityPriceTable, at time.Time) bool {
	if table == nil || table.effectiveStart.IsZero() || table.effectiveEnd.IsZero() {
		return false
	}
	start := dateOnly(table.effectiveStart, at.Location())
	end := dateOnly(table.effectiveEnd, at.Location()).Add(24*time.Hour - time.Nanosecond)
	return !at.Before(start) && !at.After(end)
}

func dateOnly(value time.Time, loc *time.Location) time.Time {
	if loc == nil {
		loc = time.Local
	}
	v := value.In(loc)
	return time.Date(v.Year(), v.Month(), v.Day(), 0, 0, 0, 0, loc)
}

func parseElectricityPriceRows(text string, effectiveStart time.Time) []electricityPriceRow {
	tokens := tokenizeElectricityPriceText(text)
	rows := make([]electricityPriceRow, 0, 8)
	mode := ""
	hasSharp := effectiveMonthHasSharp(effectiveStart)

	for i := 0; i < len(tokens); i++ {
		if i+2 < len(tokens) && tokens[i] == "单" && tokens[i+1] == "一" && tokens[i+2] == "制" {
			mode = "单一制"
			i += 2
			continue
		}
		if i+2 < len(tokens) && tokens[i] == "两" && tokens[i+1] == "部" && tokens[i+2] == "制" {
			mode = "两部制"
			i += 2
			continue
		}
		if mode == "" {
			continue
		}

		voltage, consumed := readElectricityVoltageGrade(tokens[i:])
		if voltage == "" {
			continue
		}
		i += consumed

		numberTokens := make([]string, 0, 12)
		for i+1 < len(tokens) {
			next := tokens[i+1]
			if next == "注" || next == "单" || next == "两" || isElectricityVoltageStart(tokens[i+1:]) {
				break
			}
			i++
			if isNumberToken(next) {
				numberTokens = appendElectricityNumberToken(numberTokens, next)
			}
		}

		nums := parseFloatTokens(numberTokens)
		row, ok := buildElectricityPriceRow(mode, voltage, nums, hasSharp)
		if ok {
			rows = append(rows, row)
		}
	}

	return rows
}

func buildElectricityPriceRow(mode string, voltage string, nums []float64, hasSharp bool) (electricityPriceRow, bool) {
	timeCount := 3
	if hasSharp {
		timeCount = 4
	}
	if len(nums) < timeCount+1 {
		return electricityPriceRow{}, false
	}

	end := len(nums)
	row := electricityPriceRow{
		billingMode:  mode,
		voltageGrade: voltage,
		nonTimePrice: nums[0],
	}
	if mode == "两部制" && len(nums) >= timeCount+3 {
		row.maxDemandPrice = nums[len(nums)-2]
		row.transformerCapacityPrice = nums[len(nums)-1]
		end -= 2
	}
	if end < timeCount {
		return electricityPriceRow{}, false
	}

	timePrices := nums[end-timeCount : end]
	if hasSharp {
		row.hasSharpPrice = true
		row.sharpPrice = timePrices[0]
		row.peakPrice = timePrices[1]
		row.flatPrice = timePrices[2]
		row.valleyPrice = timePrices[3]
	} else {
		row.peakPrice = timePrices[0]
		row.flatPrice = timePrices[1]
		row.valleyPrice = timePrices[2]
	}
	return row, true
}

func readElectricityVoltageGrade(tokens []string) (string, int) {
	if len(tokens) >= 3 && tokens[0] == "不满" && tokens[1] == "1" && strings.HasPrefix(tokens[2], "千伏") {
		return "不满1千伏", 2
	}
	if len(tokens) >= 2 && strings.HasPrefix(tokens[0], "1-10") && strings.HasPrefix(tokens[1], "千伏") {
		return "1-10千伏", 1
	}
	if len(tokens) >= 3 && tokens[0] == "35" && strings.HasPrefix(tokens[1], "-110") && strings.HasPrefix(tokens[2], "千伏") {
		return "35-110千伏", 2
	}
	if len(tokens) >= 2 && tokens[0] == "35-110" && strings.HasPrefix(tokens[1], "千伏") {
		return "35-110千伏", 1
	}
	if len(tokens) >= 2 && tokens[0] == "220" && strings.HasPrefix(tokens[1], "千伏及以上") {
		return "220千伏及以上", 1
	}
	return "", 0
}

func isElectricityVoltageStart(tokens []string) bool {
	if len(tokens) == 0 {
		return false
	}
	if tokens[0] == "不满" || strings.HasPrefix(tokens[0], "1-10") || tokens[0] == "35" || tokens[0] == "35-110" || tokens[0] == "220" {
		voltage, _ := readElectricityVoltageGrade(tokens)
		return voltage != ""
	}
	return false
}

func tokenizeElectricityPriceText(text string) []string {
	fields := strings.FieldsFunc(text, func(r rune) bool {
		return unicode.IsSpace(r) || r == '：' || r == ':' || r == '，' || r == ',' || r == '；' || r == ';'
	})
	tokens := make([]string, 0, len(fields))
	for _, field := range fields {
		field = strings.TrimSpace(field)
		field = strings.Trim(field, "（）()[]")
		if field != "" {
			tokens = append(tokens, field)
		}
	}
	return tokens
}

func appendElectricityNumberToken(tokens []string, next string) []string {
	if len(tokens) == 0 {
		return append(tokens, next)
	}
	last := tokens[len(tokens)-1]
	if strings.Contains(last, ".") && !strings.Contains(next, ".") {
		decimalLen := len(last) - strings.LastIndex(last, ".") - 1
		if decimalLen > 0 && decimalLen < 6 && len(next) <= 6-decimalLen {
			tokens[len(tokens)-1] = last + next
			return tokens
		}
	}
	return append(tokens, next)
}

func isNumberToken(s string) bool {
	_, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return err == nil
}

func parseFloatTokens(tokens []string) []float64 {
	nums := make([]float64, 0, len(tokens))
	for _, token := range tokens {
		v, err := strconv.ParseFloat(token, 64)
		if err == nil {
			nums = append(nums, v)
		}
	}
	return nums
}

func compactElectricityPriceText(text string) string {
	return strings.Join(strings.Fields(text), "")
}

func sliceByCompactElectricityPriceIndex(text string, compactIndex int) string {
	var compactCount int
	for i, r := range text {
		if unicode.IsSpace(r) {
			continue
		}
		if compactCount >= compactIndex {
			return text[:i]
		}
		compactCount++
	}
	return text
}

func effectiveMonthHasSharp(effectiveStart time.Time) bool {
	month := int(effectiveStart.Month())
	return month == 1 || month == 7 || month == 8 || month == 12
}

func selectDefaultElectricityPriceRow(rows []electricityPriceRow) (electricityPriceRow, bool) {
	for _, row := range rows {
		if strings.Contains(row.billingMode, "单一") && strings.Contains(row.voltageGrade, "1-10") {
			return row, true
		}
	}
	for _, row := range rows {
		if strings.Contains(row.voltageGrade, "1-10") {
			return row, true
		}
	}
	if len(rows) == 0 {
		return electricityPriceRow{}, false
	}
	return rows[0], true
}

func electricityPricePeriodAt(at time.Time) string {
	month := int(at.Month())
	minute := at.Hour()*60 + at.Minute()
	if (month == 7 || month == 8) && (inMinuteRange(minute, 11*60, 13*60) || inMinuteRange(minute, 16*60, 17*60)) {
		return "sharp"
	}
	if (month == 1 || month == 12) && inMinuteRange(minute, 18*60, 21*60) {
		return "sharp"
	}
	if inMinuteRange(minute, 10*60, 13*60) || inMinuteRange(minute, 17*60, 22*60) {
		return "peak"
	}
	if inMinuteRange(minute, 7*60, 10*60) || inMinuteRange(minute, 13*60, 17*60) || inMinuteRange(minute, 22*60, 23*60) {
		return "flat"
	}
	return "valley"
}

func inMinuteRange(value int, start int, end int) bool {
	return value >= start && value < end
}

func electricityPriceForPeriod(row electricityPriceRow, period string) (float64, bool) {
	switch period {
	case "sharp":
		if row.hasSharpPrice && row.sharpPrice > 0 {
			return row.sharpPrice, true
		}
		return row.peakPrice, row.peakPrice > 0
	case "peak":
		return row.peakPrice, row.peakPrice > 0
	case "flat":
		return row.flatPrice, row.flatPrice > 0
	case "valley":
		return row.valleyPrice, row.valleyPrice > 0
	default:
		return 0, false
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

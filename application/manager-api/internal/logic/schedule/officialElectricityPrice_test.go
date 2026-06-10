package schedule

import (
	"testing"
	"time"
)

func TestParseBeijingAgentPurchasePriceTableWithSharpPeriod(t *testing.T) {
	text := `
国网北京市电力公司
代理购电工商业用户电价表
（执行时间：
2024
年
7
月
1
日
-
2024
年
7
月
31
日）
工商
业
用电
单
一
制
不满
1
千伏
0.884835
0.409176
0.012185
0.410000
0.026306
0.027168
1.17535
0
1.175350
0.884835
0.622962
1-10
千伏
0.864835
0.390000
1.339479
1.192176
0.864835
0.578412
两
部
制
1-10
千伏
0.681335
0.206500
1.057777
0.926841
0.681335
0.435829
51
32
注
`

	table, err := parseBeijingAgentPurchasePriceTable(text)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if len(table.rows) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(table.rows))
	}
	first := table.rows[0]
	if !first.hasSharpPrice || first.sharpPrice != 1.175350 || first.peakPrice != 1.175350 || first.flatPrice != 0.884835 || first.valleyPrice != 0.622962 {
		t.Fatalf("unexpected first row: %+v", first)
	}
	selected, ok := selectDefaultElectricityPriceRow(table.rows)
	if !ok || selected.voltageGrade != "1-10千伏" || selected.peakPrice != 1.192176 {
		t.Fatalf("unexpected selected row: ok=%v row=%+v", ok, selected)
	}
}

func TestElectricityPricePeriodAt(t *testing.T) {
	loc := time.FixedZone("CST", 8*60*60)
	cases := []struct {
		at   time.Time
		want string
	}{
		{time.Date(2024, 7, 1, 11, 30, 0, 0, loc), "sharp"},
		{time.Date(2024, 7, 1, 17, 30, 0, 0, loc), "peak"},
		{time.Date(2024, 6, 1, 14, 30, 0, 0, loc), "flat"},
		{time.Date(2024, 6, 1, 23, 30, 0, 0, loc), "valley"},
	}
	for _, tc := range cases {
		if got := electricityPricePeriodAt(tc.at); got != tc.want {
			t.Fatalf("period at %s: got %s, want %s", tc.at, got, tc.want)
		}
	}
}

func TestElectricityPriceTableEffective(t *testing.T) {
	loc := time.FixedZone("CST", 8*60*60)
	table := &electricityPriceTable{
		effectiveStart: time.Date(2024, 7, 1, 0, 0, 0, 0, loc),
		effectiveEnd:   time.Date(2024, 7, 31, 0, 0, 0, 0, loc),
	}
	if !electricityPriceTableEffective(table, time.Date(2024, 7, 31, 23, 59, 0, 0, loc)) {
		t.Fatalf("expected table to be effective on end date")
	}
	if electricityPriceTableEffective(table, time.Date(2024, 8, 1, 0, 0, 0, 0, loc)) {
		t.Fatalf("expected table to be expired after end date")
	}
}

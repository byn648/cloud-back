package application

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/yanshicheng/kube-nova/application/manager-rpc/client/managerservice"
	"github.com/yanshicheng/kube-nova/application/workload-api/internal/svc"
	"github.com/yanshicheng/kube-nova/application/workload-api/internal/types"
	"github.com/yanshicheng/kube-nova/common/utils"
	"github.com/zeromicro/go-zero/core/logx"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

type AddApplicationResourceLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

const greenSchedulerName = "green-rl-scheduler"

type schedulePlanPayload struct {
	Placements []schedulePlanPlacement `json:"placements"`
}

type schedulePlanPlacement struct {
	ReplicaName string `json:"replicaName"`
	NodeName    string `json:"nodeName"`
}

func NewAddApplicationResourceLogic(ctx context.Context, svcCtx *svc.ServiceContext) *AddApplicationResourceLogic {
	return &AddApplicationResourceLogic{
		Logger: logx.WithContext(ctx),
		ctx:    ctx,
		svcCtx: svcCtx,
	}
}

func (l *AddApplicationResourceLogic) AddApplicationResource(req *types.AddApplicationResource) (resp string, err error) {
	username, ok := l.ctx.Value("username").(string)
	if !ok || username == "" {
		username = "system"
	}

	effectiveWorkspaceId := req.WorkspaceId

	// 获取工作空间资源
	workspace, err := l.svcCtx.ManagerRpc.ProjectWorkspaceGetById(l.ctx, &managerservice.GetOnecProjectWorkspaceByIdReq{
		Id: effectiveWorkspaceId,
	})
	if err != nil {
		l.Errorf("获取工作空间失败: %v", err)
		return "", fmt.Errorf("获取工作空间失败: %v", err)
	}
	// 查询项目集群
	projectCluster, err := l.svcCtx.ManagerRpc.ProjectClusterGetById(l.ctx, &managerservice.GetOnecProjectClusterByIdReq{
		Id: workspace.Data.ProjectClusterId,
	})
	if err != nil {
		l.Errorf("获取项目集群失败: %v", err)
		return "", fmt.Errorf("获取项目集群失败: %v", err)
	}

	// 查询项目
	project, err := l.svcCtx.ManagerRpc.ProjectGetById(l.ctx, &managerservice.GetOnecProjectByIdReq{
		Id: projectCluster.Data.ProjectId,
	})
	if err != nil {
		l.Errorf("获取项目失败: %v", err)
		return "", fmt.Errorf("获取项目失败: %v", err)
	}

	if err := l.validateTargetClusterWorkspaceScope(
		projectCluster.Data.ProjectId,
		workspace.Data.ClusterUuid,
		workspace.Data.Namespace,
		workspace.Data.Name,
		req.TargetClusterUuid,
	); err != nil {
		return "", err
	}

	deployClusterUuid := resolveDeployClusterUuid(req, workspace.Data.ClusterUuid)
	if deployClusterUuid == "" {
		return "", fmt.Errorf("未找到可部署的目标集群")
	}

	// 解析 YAML
	k8sObj, err := utils.ParseAndConvertK8sResource(req.ResourceYamlStr, req.ResourceType)
	if err != nil {
		l.Errorf("解析并转换 Kubernetes 资源失败: %v", err)
		return "", fmt.Errorf("解析并转换 Kubernetes 资源失败: %v", err)
	}
	l.Infof("资源解析和转换成功，类型: %s", req.ResourceType)

	// 验证 K8s 资源
	validator := utils.K8sResourceValidator{
		ExpectedNamespace: workspace.Data.Namespace,
		ExpectedName:      req.ResourceName,
	}
	if err := validator.Validate(k8sObj); err != nil {
		l.Errorf("资源验证失败: %v", err)
		return "", fmt.Errorf("资源验证失败: %v", err)
	}

	// 注入注解，包括资源级别和 Pod 模板级别
	injectAnnotations(k8sObj, req.ResourceType, &utils.AnnotationsInfo{
		ServiceName:     req.ResourceName,
		ApplicationName: req.NameCn,
		ApplicationEn:   req.NameEn,
		ProjectName:     project.Data.Name,
		ProjectUuid:     project.Data.Uuid,
		Version:         req.Version,
		Description:     req.Description,
		WorkspaceName:   workspace.Data.Name,
	})
	injectScheduleAnnotations(k8sObj, req.ResourceType, req.SchedulePlanId, deployClusterUuid)
	plannedNodes, err := applySchedulePlan(k8sObj, req.ResourceType, req.SchedulePlanJson)
	if err != nil {
		l.updateSchedulePlanStatus(req, "FAILED", fmt.Sprintf("调度计划解析失败: %v", err), username)
		return "", err
	}

	// 验证 resourceType
	if !utils.IsResourceType(req.ResourceType) {
		return "", fmt.Errorf("资源类型错误")
	}

	client, err := l.svcCtx.K8sManager.GetCluster(l.ctx, deployClusterUuid)
	if err != nil {
		l.Errorf("集群管理器获取失败: %s", err.Error())
		l.updateSchedulePlanStatus(req, "FAILED", fmt.Sprintf("获取目标集群失败: %v", err), username)
		return "", err
	}
	_, err = client.Namespaces().Get(workspace.Data.Namespace)
	if err != nil {
		l.Errorf("命名空间获取异常: %s", err.Error())
		l.updateSchedulePlanStatus(req, "FAILED", fmt.Sprintf("目标命名空间不可用: %v", err), username)
		return "", err
	}

	// 判断是否为一次性资源
	resourceTypeUpper := strings.ToUpper(req.ResourceType)
	isOneTimeResource := resourceTypeUpper == "POD" || resourceTypeUpper == "JOB"

	if isOneTimeResource {
		l.Infof("资源类型 %s 为一次性任务，直接部署到集群", req.ResourceType)

		// 部署到 K8s 集群
		l.updateSchedulePlanStatus(req, "EXECUTING", "开始按调度计划部署资源", username)
		if err := l.deployToK8sCluster(k8sObj, deployClusterUuid, workspace.Data.Namespace, req.ResourceType, plannedNodes); err != nil {
			l.Errorf("部署到 K8s 集群失败: %v", err)
			l.updateSchedulePlanStatus(req, "FAILED", fmt.Sprintf("Kubernetes 部署失败: %v", err), username)

			// 记录失败的审计日志
			_, _ = l.svcCtx.ManagerRpc.ProjectAuditLogAdd(l.ctx, &managerservice.AddOnecProjectAuditLogReq{
				WorkspaceId:  effectiveWorkspaceId,
				Title:        "创建资源",
				ActionDetail: withScheduleAuditDetail(fmt.Sprintf("创建 %s 资源失败: %s, 错误: %v", req.ResourceType, req.ResourceName, err), req, plannedNodes, effectiveWorkspaceId, deployClusterUuid),
				Status:       0,
			})

			return "", fmt.Errorf("部署到 K8s 集群失败: %v", err)
		}

		// 记录成功的审计日志
		_, auditErr := l.svcCtx.ManagerRpc.ProjectAuditLogAdd(l.ctx, &managerservice.AddOnecProjectAuditLogReq{
			WorkspaceId:  effectiveWorkspaceId,
			Title:        "创建资源",
			ActionDetail: withScheduleAuditDetail(fmt.Sprintf("创建 %s 资源: %s", req.ResourceType, req.ResourceName), req, plannedNodes, effectiveWorkspaceId, deployClusterUuid),
			Status:       1,
		})
		if auditErr != nil {
			l.Errorf("记录审计日志失败: %v", auditErr)
		}
		l.updateSchedulePlanStatus(req, "SUCCEEDED", fmt.Sprintf("资源 %s 已按调度计划部署到目标集群", req.ResourceName), username)
	} else {
		// 其他类型：创建应用和版本
		if err := utils.ValidateVersionName(req.NameEn); err != nil {
			return "", fmt.Errorf("服务英文名错误: %v", err)
		}
		if err := utils.ValidateVersionName(req.Version); err != nil {
			return "", fmt.Errorf("版本错误: %v", err)
		}

		// 添加应用
		addAppResp, err := l.svcCtx.ManagerRpc.ApplicationAdd(l.ctx, &managerservice.AddOnecProjectApplicationReq{
			WorkspaceId:  effectiveWorkspaceId,
			NameCn:       req.NameCn,
			NameEn:       req.NameEn,
			ResourceType: req.ResourceType,
			Description:  req.Description,
			CreatedBy:    username,
			UpdatedBy:    username,
		})
		if err != nil {
			l.Errorf("添加应用失败: %v", err)
			l.updateSchedulePlanStatus(req, "FAILED", fmt.Sprintf("创建应用记录失败: %v", err), username)
			return "", err
		}

		// 添加版本
		addVersionResp, err := l.svcCtx.ManagerRpc.VersionAdd(l.ctx, &managerservice.AddOnecProjectVersionReq{
			ApplicationId: addAppResp.Id,
			Version:       req.Version,
			ResourceName:  req.ResourceName,
			CreatedBy:     username,
			UpdatedBy:     username,
		})
		if err != nil {
			l.Errorf("添加版本失败: %v", err)
			l.updateSchedulePlanStatus(req, "FAILED", fmt.Sprintf("创建应用版本失败: %v", err), username)
			return "", err
		}

		// 部署到 K8s 集群
		l.updateSchedulePlanStatus(req, "EXECUTING", "开始按调度计划部署资源", username)
		if err := l.deployToK8sCluster(k8sObj, deployClusterUuid, workspace.Data.Namespace, req.ResourceType, plannedNodes); err != nil {
			l.Errorf("部署到 K8s 集群失败: %v", err)
			l.updateSchedulePlanStatus(req, "FAILED", fmt.Sprintf("Kubernetes 部署失败: %v", err), username)

			// 记录失败的审计日志
			_, _ = l.svcCtx.ManagerRpc.ProjectAuditLogAdd(l.ctx, &managerservice.AddOnecProjectAuditLogReq{
				VersionId:    addVersionResp.Id,
				Title:        "创建应用和版本",
				ActionDetail: withScheduleAuditDetail(fmt.Sprintf("创建应用和版本失败: %s-%s, 错误: %v", req.NameCn, req.Version, err), req, plannedNodes, effectiveWorkspaceId, deployClusterUuid),
				Status:       0,
			})

			return "", fmt.Errorf("部署到 K8s 集群失败: %v", err)
		}

		// 记录成功的审计日志
		_, auditErr := l.svcCtx.ManagerRpc.ProjectAuditLogAdd(l.ctx, &managerservice.AddOnecProjectAuditLogReq{
			VersionId:    addVersionResp.Id,
			Title:        "创建应用和版本",
			ActionDetail: withScheduleAuditDetail(fmt.Sprintf("创建应用和版本: %s-%s", req.NameCn, req.Version), req, plannedNodes, effectiveWorkspaceId, deployClusterUuid),
			Status:       1,
		})
		if auditErr != nil {
			l.Errorf("记录审计日志失败: %v", auditErr)
		}
		l.updateSchedulePlanStatus(req, "SUCCEEDED", fmt.Sprintf("资源 %s 已按调度计划部署到目标集群", req.ResourceName), username)
	}

	return fmt.Sprintf("资源 %s 创建成功", req.ResourceName), nil
}

// injectAnnotations 注入注解到资源，包括资源级别和 Pod 模板级别
func injectAnnotations(obj interface{}, resourceType string, info *utils.AnnotationsInfo) {

	switch strings.ToUpper(resourceType) {
	case "DEPLOYMENT":
		deployment := obj.(*appsv1.Deployment)
		utils.AddAnnotations(&deployment.ObjectMeta, info)
		utils.AddAnnotations(&deployment.Spec.Template.ObjectMeta, info)

	case "STATEFULSET":
		statefulSet := obj.(*appsv1.StatefulSet)
		utils.AddAnnotations(&statefulSet.ObjectMeta, info)
		utils.AddAnnotations(&statefulSet.Spec.Template.ObjectMeta, info)
	case "DAEMONSET":
		daemonSet := obj.(*appsv1.DaemonSet)
		utils.AddAnnotations(&daemonSet.ObjectMeta, info)
		utils.AddAnnotations(&daemonSet.Spec.Template.ObjectMeta, info)

	case "JOB":
		job := obj.(*batchv1.Job)
		utils.AddAnnotations(&job.ObjectMeta, info)
		utils.AddAnnotations(&job.Spec.Template.ObjectMeta, info)

	case "CRONJOB":
		cronJob := obj.(*batchv1.CronJob)
		utils.AddAnnotations(&cronJob.ObjectMeta, info)
		utils.AddAnnotations(&cronJob.Spec.JobTemplate.Spec.Template.ObjectMeta, info)

	case "POD":
		pod := obj.(*corev1.Pod)
		utils.AddAnnotations(&pod.ObjectMeta, info)
	}
}

func (l *AddApplicationResourceLogic) validateTargetClusterWorkspaceScope(
	projectId uint64,
	currentClusterUuid string,
	namespace string,
	workspaceName string,
	targetClusterUuid string,
) error {
	targetClusterUuid = strings.TrimSpace(targetClusterUuid)
	if targetClusterUuid == "" || targetClusterUuid == strings.TrimSpace(currentClusterUuid) {
		return nil
	}
	if projectId == 0 {
		return fmt.Errorf("项目 ID 为空，无法校验目标集群")
	}

	clusterResp, err := l.svcCtx.ManagerRpc.ProjectClusterSearch(l.ctx, &managerservice.SearchOnecProjectClusterReq{
		ProjectId:   projectId,
		ClusterUuid: targetClusterUuid,
	})
	if err != nil {
		l.Errorf("校验目标集群失败: projectId=%d targetClusterUuid=%s err=%v", projectId, targetClusterUuid, err)
		return fmt.Errorf("校验目标集群失败: %v", err)
	}

	var targetProjectClusterId uint64
	for _, cluster := range clusterResp.GetData() {
		if cluster != nil && strings.TrimSpace(cluster.GetClusterUuid()) == targetClusterUuid {
			targetProjectClusterId = cluster.GetId()
			break
		}
	}
	if targetProjectClusterId == 0 {
		return fmt.Errorf("目标集群不在当前项目授权范围内")
	}

	workspaceResp, err := l.svcCtx.ManagerRpc.ProjectWorkspaceSearch(l.ctx, &managerservice.SearchOnecProjectWorkspaceReq{
		ProjectClusterId: targetProjectClusterId,
		Name:             workspaceName,
		Namespace:        namespace,
	})
	if err != nil {
		l.Errorf("校验目标集群工作空间绑定失败: targetProjectClusterId=%d namespace=%s workspace=%s err=%v", targetProjectClusterId, namespace, workspaceName, err)
		return fmt.Errorf("校验目标集群工作空间绑定失败: %v", err)
	}

	for _, workspace := range workspaceResp.GetData() {
		if workspace != nil && workspace.GetNamespace() == namespace && workspace.GetName() == workspaceName {
			return nil
		}
	}
	return fmt.Errorf("目标集群未绑定当前工作空间")
}

func resolveDeployClusterUuid(req *types.AddApplicationResource, workspaceClusterUuid string) string {
	if req == nil {
		return strings.TrimSpace(workspaceClusterUuid)
	}
	if strings.TrimSpace(req.TargetClusterUuid) != "" {
		return strings.TrimSpace(req.TargetClusterUuid)
	}
	if strings.TrimSpace(req.ClusterUuid) != "" {
		return strings.TrimSpace(req.ClusterUuid)
	}
	return strings.TrimSpace(workspaceClusterUuid)
}

func injectScheduleAnnotations(obj interface{}, resourceType, schedulePlanId, targetClusterUuid string) {
	if strings.TrimSpace(schedulePlanId) == "" && strings.TrimSpace(targetClusterUuid) == "" {
		return
	}

	apply := func(meta *metav1.ObjectMeta) {
		if meta.Annotations == nil {
			meta.Annotations = map[string]string{}
		}
		if strings.TrimSpace(schedulePlanId) != "" {
			meta.Annotations["green-scheduler.kube-nova.io/plan-id"] = strings.TrimSpace(schedulePlanId)
		}
		if strings.TrimSpace(targetClusterUuid) != "" {
			meta.Annotations["green-scheduler.kube-nova.io/target-cluster"] = strings.TrimSpace(targetClusterUuid)
		}
	}

	switch strings.ToUpper(resourceType) {
	case "DEPLOYMENT":
		deployment := obj.(*appsv1.Deployment)
		apply(&deployment.ObjectMeta)
		apply(&deployment.Spec.Template.ObjectMeta)
	case "STATEFULSET":
		statefulSet := obj.(*appsv1.StatefulSet)
		apply(&statefulSet.ObjectMeta)
		apply(&statefulSet.Spec.Template.ObjectMeta)
	case "DAEMONSET":
		daemonSet := obj.(*appsv1.DaemonSet)
		apply(&daemonSet.ObjectMeta)
		apply(&daemonSet.Spec.Template.ObjectMeta)
	case "JOB":
		job := obj.(*batchv1.Job)
		apply(&job.ObjectMeta)
		apply(&job.Spec.Template.ObjectMeta)
	case "CRONJOB":
		cronJob := obj.(*batchv1.CronJob)
		apply(&cronJob.ObjectMeta)
		apply(&cronJob.Spec.JobTemplate.Spec.Template.ObjectMeta)
	case "POD":
		pod := obj.(*corev1.Pod)
		apply(&pod.ObjectMeta)
	}
}

func applySchedulePlan(obj interface{}, resourceType string, rawPlan string) ([]string, error) {
	placements, err := parseSchedulePlanPlacements(rawPlan)
	if err != nil {
		return nil, err
	}
	if len(placements) == 0 {
		return nil, nil
	}

	nodes := make([]string, 0, len(placements))
	for _, placement := range placements {
		nodeName := strings.TrimSpace(placement.NodeName)
		if nodeName == "" {
			return nil, fmt.Errorf("调度计划中存在空节点")
		}
		nodes = append(nodes, nodeName)
	}

	switch strings.ToUpper(resourceType) {
	case "DEPLOYMENT":
		deployment := obj.(*appsv1.Deployment)
		deployment.Spec.Template.Spec.SchedulerName = greenSchedulerName
		if deployment.Spec.Template.Annotations == nil {
			deployment.Spec.Template.Annotations = map[string]string{}
		}
		deployment.Spec.Template.Annotations["green-scheduler.kube-nova.io/target-nodes"] = strings.Join(nodes, ",")
		return nodes, nil
	default:
		return nil, nil
	}
}

func parseSchedulePlanPlacements(rawPlan string) ([]schedulePlanPlacement, error) {
	if strings.TrimSpace(rawPlan) == "" {
		return nil, nil
	}
	var plan schedulePlanPayload
	if err := json.Unmarshal([]byte(rawPlan), &plan); err != nil {
		return nil, fmt.Errorf("调度计划解析失败: %v", err)
	}
	return plan.Placements, nil
}

func withScheduleAuditDetail(base string, req *types.AddApplicationResource, plannedNodes []string, workspaceId uint64, targetClusterUuid string) string {
	if req == nil || (strings.TrimSpace(req.SchedulePlanId) == "" && len(plannedNodes) == 0) {
		return base
	}
	parts := []string{base}
	if strings.TrimSpace(req.SchedulePlanId) != "" {
		parts = append(parts, fmt.Sprintf("调度计划ID=%s", strings.TrimSpace(req.SchedulePlanId)))
	}
	if strings.TrimSpace(targetClusterUuid) != "" {
		parts = append(parts, fmt.Sprintf("目标集群=%s", strings.TrimSpace(targetClusterUuid)))
	}
	if workspaceId > 0 {
		parts = append(parts, fmt.Sprintf("工作空间ID=%d", workspaceId))
	}
	if len(plannedNodes) > 0 {
		parts = append(parts, fmt.Sprintf("目标节点分布=%s", strings.Join(plannedNodes, ",")))
	}
	return strings.Join(parts, "；")
}

func (l *AddApplicationResourceLogic) updateSchedulePlanStatus(req *types.AddApplicationResource, status, message, updatedBy string) {
	if req == nil || strings.TrimSpace(req.SchedulePlanId) == "" {
		return
	}
	_, err := l.svcCtx.ManagerRpc.SchedulePlanUpdateStatus(l.ctx, &managerservice.UpdateSchedulePlanStatusReq{
		PlanId:         strings.TrimSpace(req.SchedulePlanId),
		Status:         strings.TrimSpace(status),
		ExecuteMessage: strings.TrimSpace(message),
		ResourceType:   strings.ToUpper(strings.TrimSpace(req.ResourceType)),
		ResourceName:   strings.TrimSpace(req.ResourceName),
		UpdatedBy:      strings.TrimSpace(updatedBy),
	})
	if err != nil {
		l.Errorf("update schedule plan status failed: planId=%s status=%s err=%v", req.SchedulePlanId, status, err)
	}
}

func (l *AddApplicationResourceLogic) bindDeploymentPods(
	kubeClient kubernetes.Interface,
	deployment *appsv1.Deployment,
	plannedNodes []string,
) error {
	if kubeClient == nil {
		return fmt.Errorf("Kubernetes 客户端为空，无法执行调度绑定")
	}
	if deployment == nil || deployment.Spec.Selector == nil {
		return fmt.Errorf("Deployment 选择器为空，无法定位待绑定 Pod")
	}
	if len(plannedNodes) == 0 {
		return nil
	}

	selector, err := metav1.LabelSelectorAsSelector(deployment.Spec.Selector)
	if err != nil {
		return fmt.Errorf("解析 Deployment Pod 选择器失败: %v", err)
	}

	pods, err := l.waitForUnboundSchedulePods(
		kubeClient,
		deployment.Namespace,
		selector.String(),
		len(plannedNodes),
	)
	if err != nil {
		return err
	}

	for index, pod := range pods {
		nodeName := plannedNodes[index]
		binding := &corev1.Binding{
			ObjectMeta: metav1.ObjectMeta{
				Name:      pod.Name,
				Namespace: pod.Namespace,
			},
			Target: corev1.ObjectReference{
				Kind: "Node",
				Name: nodeName,
			},
		}
		if err := kubeClient.CoreV1().Pods(pod.Namespace).Bind(l.ctx, binding, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("绑定 Pod %s/%s 到节点 %s 失败: %v", pod.Namespace, pod.Name, nodeName, err)
		}
		l.Infof("调度计划绑定 Pod %s/%s 到节点 %s", pod.Namespace, pod.Name, nodeName)
	}

	return nil
}

func (l *AddApplicationResourceLogic) waitForUnboundSchedulePods(
	kubeClient kubernetes.Interface,
	namespace string,
	labelSelector string,
	expected int,
) ([]corev1.Pod, error) {
	deadline := time.Now().Add(45 * time.Second)
	for {
		podList, err := kubeClient.CoreV1().Pods(namespace).List(l.ctx, metav1.ListOptions{
			LabelSelector: labelSelector,
		})
		if err != nil {
			return nil, fmt.Errorf("查询待绑定 Pod 失败: %v", err)
		}

		pods := make([]corev1.Pod, 0, expected)
		for _, pod := range podList.Items {
			if pod.DeletionTimestamp != nil {
				continue
			}
			if pod.Spec.SchedulerName != greenSchedulerName {
				continue
			}
			if strings.TrimSpace(pod.Spec.NodeName) != "" {
				continue
			}
			pods = append(pods, pod)
		}

		if len(pods) >= expected {
			sort.SliceStable(pods, func(i, j int) bool {
				if !pods[i].CreationTimestamp.Equal(&pods[j].CreationTimestamp) {
					return pods[i].CreationTimestamp.Before(&pods[j].CreationTimestamp)
				}
				return pods[i].Name < pods[j].Name
			})
			return pods[:expected], nil
		}

		if time.Now().After(deadline) {
			return nil, fmt.Errorf("等待调度计划 Pod 超时：期望 %d 个，当前 %d 个", expected, len(pods))
		}

		select {
		case <-l.ctx.Done():
			return nil, l.ctx.Err()
		case <-time.After(1 * time.Second):
		}
	}
}

// deployToK8sCluster 部署资源到 K8s 集群
func (l *AddApplicationResourceLogic) deployToK8sCluster(
	obj interface{},
	clusterUuid string,
	namespace string,
	resourceType string,
	plannedNodes []string,
) error {
	client, err := l.svcCtx.K8sManager.GetCluster(l.ctx, clusterUuid)
	if err != nil {
		return fmt.Errorf("获取集群客户端失败: %v", err)
	}

	resourceType = strings.ToUpper(resourceType)

	switch resourceType {
	case "DEPLOYMENT":
		deployment := obj.(*appsv1.Deployment)
		l.Infof("准备部署 Deployment: %s 到集群: %s, namespace: %s", deployment.Name, clusterUuid, namespace)
		created, err := client.Deployment().Create(deployment)
		if err != nil {
			return fmt.Errorf("创建 Deployment 失败: %v", err)
		}
		if len(plannedNodes) > 0 {
			if err := l.bindDeploymentPods(client.GetKubeClient(), created, plannedNodes); err != nil {
				return err
			}
		}
		l.Infof("Deployment %s 创建成功", deployment.Name)

	case "STATEFULSET":
		statefulSet := obj.(*appsv1.StatefulSet)
		l.Infof("准备部署 StatefulSet: %s 到集群: %s, namespace: %s", statefulSet.Name, clusterUuid, namespace)
		_, err := client.StatefulSet().Create(statefulSet)
		if err != nil {
			return fmt.Errorf("创建 StatefulSet 失败: %v", err)
		}
		l.Infof("StatefulSet %s 创建成功", statefulSet.Name)

	case "DAEMONSET":
		daemonSet := obj.(*appsv1.DaemonSet)
		l.Infof("准备部署 DaemonSet: %s 到集群: %s, namespace: %s", daemonSet.Name, clusterUuid, namespace)
		_, err := client.DaemonSet().Create(daemonSet)
		if err != nil {
			return fmt.Errorf("创建 DaemonSet 失败: %v", err)
		}
		l.Infof("DaemonSet %s 创建成功", daemonSet.Name)

	case "JOB":
		job := obj.(*batchv1.Job)
		l.Infof("准备部署 Job: %s 到集群: %s, namespace: %s", job.Name, clusterUuid, namespace)
		_, err := client.Job().Create(job)
		if err != nil {
			return fmt.Errorf("创建 Job 失败: %v", err)
		}
		l.Infof("Job %s 创建成功", job.Name)

	case "CRONJOB":
		cronJob := obj.(*batchv1.CronJob)
		l.Infof("准备部署 CronJob: %s 到集群: %s, namespace: %s", cronJob.Name, clusterUuid, namespace)
		_, err := client.CronJob().Create(cronJob)
		if err != nil {
			return fmt.Errorf("创建 CronJob 失败: %v", err)
		}
		l.Infof("CronJob %s 创建成功", cronJob.Name)

	case "POD":
		pod := obj.(*corev1.Pod)
		l.Infof("准备部署 Pod: %s 到集群: %s, namespace: %s", pod.Name, clusterUuid, namespace)
		_, err := client.Pods().Create(pod)
		if err != nil {
			return fmt.Errorf("创建 Pod 失败: %v", err)
		}
		l.Infof("Pod %s 创建成功", pod.Name)

	default:
		return fmt.Errorf("不支持的资源类型: %s", resourceType)
	}

	return nil
}

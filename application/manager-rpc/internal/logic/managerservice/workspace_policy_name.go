package managerservicelogic

import "strings"

func workspacePolicyName(namespace string) string {
	namespace = strings.TrimSpace(namespace)
	if namespace == "" {
		return "ikubeops"
	}
	return "ikubeops-" + namespace
}

func legacyWorkspacePolicyName(workspaceName string) string {
	workspaceName = strings.TrimSpace(workspaceName)
	if workspaceName == "" {
		return "ikubeops"
	}
	return "ikubeops" + workspaceName
}

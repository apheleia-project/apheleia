package e2e

const (
	testNamespace           = "apheleia-test-namespace-"
	maxNameLength           = 63
	randomLength            = 5
	maxGeneratedNameLength  = maxNameLength - randomLength
	gitCloneTaskUrl         = "https://raw.githubusercontent.com/tektoncd/catalog/main/task/git-clone/0.9/git-clone.yaml"
	minikubeGitCloneTaskUrl = "https://raw.githubusercontent.com/tektoncd/catalog/main/task/git-clone/0.9/git-clone.yaml"
)

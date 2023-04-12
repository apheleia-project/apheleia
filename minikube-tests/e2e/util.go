package e2e

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"github.com/redhat-appstudio/jvm-build-service/pkg/reconciler/jbsconfig"
	"html/template"
	"io"
	v13 "k8s.io/api/apps/v1"
	ev12 "k8s.io/api/events/v1"
	v1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/intstr"
	ev1 "k8s.io/client-go/kubernetes/typed/events/v1"
	"net/http"
	"os"
	"path/filepath"
	ctrl "sigs.k8s.io/controller-runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/redhat-appstudio/jvm-build-service/pkg/apis/jvmbuildservice/v1alpha1"
	jvmclientset "github.com/redhat-appstudio/jvm-build-service/pkg/client/clientset/versioned"
	"github.com/redhat-appstudio/jvm-build-service/pkg/reconciler/artifactbuild"
	"github.com/tektoncd/pipeline/pkg/apis/pipeline/v1beta1"
	pipelineclientset "github.com/tektoncd/pipeline/pkg/client/clientset/versioned"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	utilrand "k8s.io/apimachinery/pkg/util/rand"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/cli-runtime/pkg/printers"
	kubeset "k8s.io/client-go/kubernetes"
	v12 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
)

func generateName(base string) string {
	if len(base) > maxGeneratedNameLength {
		base = base[:maxGeneratedNameLength]
	}
	return fmt.Sprintf("%s%s", base, utilrand.String(randomLength))
}

func dumpBadEvents(ta *testArgs) {
	eventClient := kubeClient.EventsV1().Events(ta.ns)
	eventList, err := eventClient.List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		ta.Logf(fmt.Sprintf("error listing events: %s", err.Error()))
		return
	}
	ta.Logf(fmt.Sprintf("dumpBadEvents have %d items in total list", len(eventList.Items)))
	for _, event := range eventList.Items {
		if event.Type == corev1.EventTypeNormal {
			continue
		}
		ta.Logf(fmt.Sprintf("non-normal event reason %s about obj %s:%s message %s", event.Reason, event.Regarding.Kind, event.Regarding.Name, event.Note))
	}
}

func dumpNodes(ta *testArgs) {
	nodeClient := kubeClient.CoreV1().Nodes()
	nodeList, err := nodeClient.List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		ta.Logf(fmt.Sprintf("error listin nodes: %s", err.Error()))
		return
	}
	ta.Logf(fmt.Sprintf("dumpNodes found %d nodes in list", len(nodeList.Items)))
	for _, node := range nodeList.Items {
		_, master := node.Labels["node-role.kubernetes.io/master"]
		if master {
			ta.Logf(fmt.Sprintf("Node %s is master node", node.Name))
		}
		if node.Status.Allocatable.Cpu() == nil {
			ta.Logf(fmt.Sprintf("Node %s does not have allocatable cpu", node.Name))
			continue
		}
		if node.Status.Allocatable.Memory() == nil {
			ta.Logf(fmt.Sprintf("Node %s does not have allocatable mem", node.Name))
			continue
		}
		if node.Status.Capacity.Cpu() == nil {
			ta.Logf(fmt.Sprintf("Node %s does not have capacity cpu", node.Name))
			continue
		}
		if node.Status.Capacity.Memory() == nil {
			ta.Logf(fmt.Sprintf("Node %s does not have capacity mem", node.Name))
			continue
		}
		alloccpu := node.Status.Allocatable.Cpu()
		allocmem := node.Status.Allocatable.Memory()
		capaccpu := node.Status.Capacity.Cpu()
		capacmem := node.Status.Capacity.Memory()
		ta.Logf(fmt.Sprintf("Node %s allocatable CPU %s allocatable mem %s capacity CPU %s capacitymem %s",
			node.Name,
			alloccpu.String(),
			allocmem.String(),
			capaccpu.String(),
			capacmem.String()))
	}
}

func debugAndFailTest(ta *testArgs, failMsg string) {
	GenerateStatusReport(ta.ns, jvmClient, kubeClient, tektonClient)
	dumpBadEvents(ta)
	ta.t.Fatalf(failMsg)

}

func commonSetup(t *testing.T, ta *testArgs, gitCloneUrl string) *testArgs {

	if ta == nil {
		ta = &testArgs{
			t:        t,
			timeout:  time.Minute * 30,
			interval: time.Second * 10,
		}
	}
	setupClients(ta.t)

	if len(ta.ns) == 0 {
		ta.ns = generateName(testNamespace)
		namespace := &corev1.Namespace{}
		namespace.Name = ta.ns
		_, err := kubeClient.CoreV1().Namespaces().Create(context.Background(), namespace, metav1.CreateOptions{})

		if err != nil {
			debugAndFailTest(ta, fmt.Sprintf("%#v", err))
		}

		if err != nil {
			debugAndFailTest(ta, fmt.Sprintf("%#v", err))
		}
	}

	eventClient := kubeClient.EventsV1().Events(ta.ns)
	go watchEvents(eventClient, ta)
	dumpNodes(ta)

	cm := corev1.ConfigMap{}
	cm.Namespace = ta.ns
	cm.Name = "apheleia-config"
	cm.Data = map[string]string{"maven-repo": "https://todo.com", "aws-owner": "todo", "aws-domain": "todo"}
	_, err := kubeClient.CoreV1().ConfigMaps(ta.ns).Create(context.Background(), &cm, metav1.CreateOptions{})
	if err != nil {
		debugAndFailTest(ta, err.Error())
	}

	// have seen delays in CRD presence along with missing pipeline SA
	err = wait.PollImmediate(1*time.Second, 1*time.Minute, func() (done bool, err error) {
		_, err = apiextensionClient.ApiextensionsV1().CustomResourceDefinitions().Get(context.TODO(), "tasks.tekton.dev", metav1.GetOptions{})
		if err != nil {
			ta.Logf(fmt.Sprintf("get of task CRD: %s", err.Error()))
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		debugAndFailTest(ta, "task CRD not present in timely fashion")
	}

	ta.gitClone = &v1beta1.Task{}
	obj := streamRemoteYamlToTektonObj(gitCloneUrl, ta.gitClone, ta)
	var ok bool
	ta.gitClone, ok = obj.(*v1beta1.Task)
	if !ok {
		debugAndFailTest(ta, fmt.Sprintf("%s did not produce a task: %#v", gitCloneTaskUrl, obj))
	}
	ta.gitClone, err = tektonClient.TektonV1beta1().Tasks(ta.ns).Create(context.TODO(), ta.gitClone, metav1.CreateOptions{})
	if err != nil {
		debugAndFailTest(ta, err.Error())
	}

	path, err := os.Getwd()
	if err != nil {
		debugAndFailTest(ta, err.Error())
	}

	mavenYamlPath := filepath.Join(path, "..", "..", "hack", "example", "maven.yaml")
	ta.maven = &v1beta1.Task{}
	obj = streamFileYamlToTektonObj(mavenYamlPath, ta.maven, ta)
	ta.maven, ok = obj.(*v1beta1.Task)
	if !ok {
		debugAndFailTest(ta, fmt.Sprintf("file %s did not produce a task: %#v", mavenYamlPath, obj))
	}
	// override images if need be
	quayUsername, _ := os.LookupEnv("QUAY_USERNAME")
	analyserImage := os.Getenv("JVM_BUILD_SERVICE_REQPROCESSOR_IMAGE")
	if len(analyserImage) > 0 {
		ta.Logf(fmt.Sprintf("PR analyzer image: %s", analyserImage))
		for i, step := range ta.maven.Spec.Steps {
			if step.Name != "analyse-dependencies" {
				continue
			}
			ta.Logf(fmt.Sprintf("Updating analyse-dependencies step with image %s", analyserImage))
			ta.maven.Spec.Steps[i].Image = analyserImage
		}
	} else if len(quayUsername) > 0 {
		image := "quay.io/" + quayUsername + "/hacbs-jvm-build-request-processor:dev"
		for i, step := range ta.maven.Spec.Steps {
			if step.Name != "analyse-dependencies" {
				continue
			}
			ta.Logf(fmt.Sprintf("Updating analyse-dependencies step with image %s", image))
			ta.maven.Spec.Steps[i].Image = image
			if strings.Contains(image, "minikube") {
				ta.maven.Spec.Steps[i].ImagePullPolicy = corev1.PullNever
				ta.Logf("Setting pull policy to never for minikube tests")
			}
		}
	}
	ta.maven, err = tektonClient.TektonV1beta1().Tasks(ta.ns).Create(context.TODO(), ta.maven, metav1.CreateOptions{})
	if err != nil {
		debugAndFailTest(ta, err.Error())
	}
	pipelineYamlPath := filepath.Join(path, "..", "..", "hack", "example", "pipeline.yaml")
	ta.pipeline = &v1beta1.Pipeline{}
	obj = streamFileYamlToTektonObj(pipelineYamlPath, ta.pipeline, ta)
	ta.pipeline, ok = obj.(*v1beta1.Pipeline)
	if !ok {
		debugAndFailTest(ta, fmt.Sprintf("file %s did not produce a pipeline: %#v", pipelineYamlPath, obj))
	}
	ta.pipeline, err = tektonClient.TektonV1beta1().Pipelines(ta.ns).Create(context.TODO(), ta.pipeline, metav1.CreateOptions{})
	if err != nil {
		debugAndFailTest(ta, err.Error())
	}
	return ta
}

func waitForCache(ta *testArgs) error {
	err := wait.PollImmediate(10*time.Second, 5*time.Minute, func() (done bool, err error) {
		cache, err := kubeClient.AppsV1().Deployments(ta.ns).Get(context.TODO(), v1alpha1.CacheDeploymentName, metav1.GetOptions{})
		if err != nil {
			ta.Logf(fmt.Sprintf("get of cache: %s", err.Error()))
			return false, nil
		}
		if cache.Status.AvailableReplicas > 0 {
			ta.Logf("Cache is available")
			return true, nil
		}
		for _, cond := range cache.Status.Conditions {
			if cond.Type == v13.DeploymentProgressing && cond.Status == "False" {
				return false, errors.New("cache deployment failed")
			}

		}
		ta.Logf("Cache is progressing")
		return false, nil
	})
	if err != nil {
		debugAndFailTest(ta, "cache not present in timely fashion")
	}
	return err
}

func bothABsAndDBsGenerated(ta *testArgs) (bool, error) {
	abList, err := jvmClient.JvmbuildserviceV1alpha1().ArtifactBuilds(ta.ns).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		ta.Logf(fmt.Sprintf("error listing artifactbuilds: %s", err.Error()))
		return false, nil
	}
	gotABs := false
	if len(abList.Items) > 0 {
		gotABs = true
	}
	dbList, err := jvmClient.JvmbuildserviceV1alpha1().DependencyBuilds(ta.ns).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		ta.Logf(fmt.Sprintf("error listing dependencybuilds: %s", err.Error()))
		return false, nil
	}
	gotDBs := false
	if len(dbList.Items) > 0 {
		gotDBs = true
	}
	if gotABs && gotDBs {
		return true, nil
	}
	return false, nil
}

//func projectCleanup(ta *testArgs) {
//	projectClient.ProjectV1().Projects().Delete(context.Background(), ta.ns, metav1.DeleteOptions{})
//}

func decodeBytesToTektonObjbytes(bytes []byte, obj runtime.Object, ta *testArgs) runtime.Object {
	decodingScheme := runtime.NewScheme()
	utilruntime.Must(v1beta1.AddToScheme(decodingScheme))
	decoderCodecFactory := serializer.NewCodecFactory(decodingScheme)
	decoder := decoderCodecFactory.UniversalDecoder(v1beta1.SchemeGroupVersion)
	err := runtime.DecodeInto(decoder, bytes, obj)
	if err != nil {
		debugAndFailTest(ta, err.Error())
	}
	return obj
}

func encodeToYaml(obj runtime.Object) string {

	y := printers.YAMLPrinter{}
	b := bytes.Buffer{}
	_ = y.PrintObj(obj, &b)
	return b.String()
}

func streamRemoteYamlToTektonObj(url string, obj runtime.Object, ta *testArgs) runtime.Object {
	resp, err := http.Get(url) //#nosec G107
	if err != nil {
		debugAndFailTest(ta, err.Error())
	}
	defer func(Body io.ReadCloser) {
		_ = Body.Close()
	}(resp.Body)
	bytes, err := io.ReadAll(resp.Body)
	if err != nil {
		debugAndFailTest(ta, err.Error())
	}
	return decodeBytesToTektonObjbytes(bytes, obj, ta)
}

func streamFileYamlToTektonObj(path string, obj runtime.Object, ta *testArgs) runtime.Object {
	bytes, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		debugAndFailTest(ta, err.Error())
	}
	return decodeBytesToTektonObjbytes(bytes, obj, ta)
}

//go:embed report.html
var reportTemplate string

// dumping the logs slows down generation
// when working on the report you might want to turn it off
// this should always be true in the committed code though
const DUMP_LOGS = true

func GenerateStatusReport(namespace string, jvmClient *jvmclientset.Clientset, kubeClient *kubeset.Clientset, pipelineClient *pipelineclientset.Clientset) {

	directory := os.Getenv("ARTIFACT_DIR")
	if directory == "" {
		directory = "/tmp/jvm-build-service-report"
	} else {
		directory = directory + "/jvm-build-service-report"
	}
	err := os.MkdirAll(directory, 0755) //#nosec G306 G301
	if err != nil {
		panic(err)
	}
	podClient := kubeClient.CoreV1().Pods(namespace)
	podList, err := podClient.List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		panic(err)
	}
	pipelineList, err := pipelineClient.TektonV1beta1().PipelineRuns(namespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		panic(err)
	}
	artifact := ArtifactReportData{}
	dependency := DependencyReportData{}
	dependencyBuildClient := jvmClient.JvmbuildserviceV1alpha1().DependencyBuilds(namespace)
	artifactBuilds, err := jvmClient.JvmbuildserviceV1alpha1().ArtifactBuilds(namespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		panic(err)
	}
	for _, ab := range artifactBuilds.Items {
		localDir := ab.Status.State + "/" + ab.Name
		tmp := ab
		createdBy := ""
		if ab.Annotations != nil {
			for k, v := range ab.Annotations {
				if strings.HasPrefix(k, artifactbuild.DependencyBuildContaminatedBy) {
					createdBy = " (created by build " + v + ")"
				}
			}
		}
		message := ""
		if ab.Status.State != v1alpha1.ArtifactBuildStateComplete {
			message = " " + ab.Status.Message
		}
		instance := &ReportInstanceData{Name: ab.Name + createdBy + message, State: ab.Status.State, Yaml: encodeToYaml(&tmp)}
		artifact.Instances = append(artifact.Instances, instance)
		artifact.Total++
		print(ab.Status.State + "\n")
		switch ab.Status.State {
		case v1alpha1.ArtifactBuildStateComplete:
			artifact.Complete++
		case v1alpha1.ArtifactBuildStateFailed:
			artifact.Failed++
		case v1alpha1.ArtifactBuildStateMissing:
			artifact.Missing++
		default:
			artifact.Other++
		}

		_ = os.MkdirAll(directory+"/"+localDir, 0755) //#nosec G306 G301
		for _, pod := range podList.Items {
			if strings.HasPrefix(pod.Name, ab.Name) {
				logFile := dumpPod(pod, directory, localDir, podClient, true)
				instance.Logs = append(instance.Logs, logFile...)
			}
		}
		for _, pipelineRun := range pipelineList.Items {
			if strings.HasPrefix(pipelineRun.Name, ab.Name) {
				t := pipelineRun
				yaml := encodeToYaml(&t)
				target := directory + "/" + localDir + "-" + "pipeline-" + t.Name
				err := os.WriteFile(target, []byte(yaml), 0644) //#nosec G306)
				if err != nil {
					print(fmt.Sprintf("Failed to write pipleine file %s: %s", target, err))
				}
				instance.Logs = append(instance.Logs, localDir+"-"+"pipeline-"+t.Name)
			}
		}
	}
	sort.Sort(SortableArtifact(artifact.Instances))

	dependencyBuilds, err := dependencyBuildClient.List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		panic(err)
	}
	for _, db := range dependencyBuilds.Items {
		dependency.Total++
		localDir := db.Status.State + "/" + db.Name
		tmp := db
		tool := "maven"
		if db.Status.CurrentBuildRecipe != nil {
			tool = db.Status.CurrentBuildRecipe.Tool
		}
		if db.Status.FailedVerification {
			tool += " (FAILED VERIFICATION)"
		}
		instance := &ReportInstanceData{State: db.Status.State, Yaml: encodeToYaml(&tmp), Name: fmt.Sprintf("%s @{%s} (%s) %s", db.Spec.ScmInfo.SCMURL, db.Spec.ScmInfo.Tag, db.Name, tool)}
		dependency.Instances = append(dependency.Instances, instance)
		print(db.Status.State + "\n")
		switch db.Status.State {
		case v1alpha1.DependencyBuildStateComplete:
			dependency.Complete++
		case v1alpha1.DependencyBuildStateFailed:
			dependency.Failed++
		case v1alpha1.DependencyBuildStateContaminated:
			dependency.Contaminated++
		case v1alpha1.DependencyBuildStateBuilding:
			dependency.Building++
		default:
			dependency.Other++
		}
		_ = os.MkdirAll(directory+"/"+localDir, 0755) //#nosec G306 G301
		for index, docker := range db.Status.DiagnosticDockerFiles {

			localPart := localDir + "-docker-" + strconv.Itoa(index) + ".txt"
			fileName := directory + "/" + localPart
			err = os.WriteFile(fileName, []byte(docker), 0644) //#nosec G306
			if err != nil {
				print(fmt.Sprintf("Failed to write docker filer %s: %s", fileName, err))
			} else {
				instance.Logs = append(instance.Logs, localPart)
			}
		}
		for _, pod := range podList.Items {
			if strings.HasPrefix(pod.Name, db.Name) {
				logFile := dumpPod(pod, directory, localDir, podClient, true)
				instance.Logs = append(instance.Logs, logFile...)
			}
		}
		for _, pipelineRun := range pipelineList.Items {
			if strings.HasPrefix(pipelineRun.Name, db.Name) {
				t := pipelineRun
				yaml := encodeToYaml(&t)
				target := directory + "/" + localDir + "-" + "pipeline-" + t.Name
				err := os.WriteFile(target, []byte(yaml), 0644) //#nosec G306)
				if err != nil {
					print(fmt.Sprintf("Failed to write pipleine file %s: %s", target, err))
				}
				instance.Logs = append(instance.Logs, localDir+"-"+"pipeline-"+t.Name)
			}
		}
	}
	sort.Sort(SortableArtifact(dependency.Instances))

	report := directory + "/index.html"

	data := ReportData{
		Artifact:   artifact,
		Dependency: dependency,
	}

	t, err := template.New("report").Parse(reportTemplate)
	if err != nil {
		panic(err)
	}
	buf := new(bytes.Buffer)
	err = t.Execute(buf, data)
	if err != nil {
		panic(err)
	}

	err = os.WriteFile(report, buf.Bytes(), 0644) //#nosec G306
	if err != nil {
		panic(err)
	}
	print("Created report file://" + report + "\n")
}

func innerDumpPod(req *rest.Request, baseDirectory, localDirectory, podName, containerName string, skipSkipped bool) error {
	var readCloser io.ReadCloser
	var err error
	readCloser, err = req.Stream(context.TODO())
	if err != nil {
		print(fmt.Sprintf("error getting pod logs for container %s: %s", containerName, err.Error()))
		return err
	}
	defer func(readCloser io.ReadCloser) {
		err := readCloser.Close()
		if err != nil {
			print(fmt.Sprintf("Failed to close ReadCloser reading pod logs for container %s: %s", containerName, err.Error()))
		}
	}(readCloser)
	var b []byte
	b, err = io.ReadAll(readCloser)
	if skipSkipped && len(b) < 1000 {
		if strings.Contains(string(b), "Skipping step because a previous step failed") {
			return errors.New("the step failed")
		}
	}
	if err != nil {
		print(fmt.Sprintf("error reading pod stream %s", err.Error()))
		return err
	}
	directory := baseDirectory + "/" + localDirectory
	err = os.MkdirAll(directory, 0755) //#nosec G306 G301
	if err != nil {
		print(fmt.Sprintf("Failed to create artifact dir %s: %s", directory, err))
		return err
	}
	localPart := localDirectory + podName + "-" + containerName
	fileName := baseDirectory + "/" + localPart
	err = os.WriteFile(fileName, b, 0644) //#nosec G306
	if err != nil {
		print(fmt.Sprintf("Failed artifact dir %s: %s", directory, err))
		return err
	}
	return nil
}

func dumpPod(pod corev1.Pod, baseDirectory string, localDirectory string, kubeClient v12.PodInterface, skipSkipped bool) []string {
	if !DUMP_LOGS {
		return []string{}
	}
	containers := []corev1.Container{}
	containers = append(containers, pod.Spec.InitContainers...)
	containers = append(containers, pod.Spec.Containers...)
	ret := []string{}
	for _, container := range containers {
		req := kubeClient.GetLogs(pod.Name, &corev1.PodLogOptions{Container: container.Name})
		err := innerDumpPod(req, baseDirectory, localDirectory, pod.Name, container.Name, skipSkipped)
		if err != nil {
			continue
		}
		ret = append(ret, localDirectory+pod.Name+"-"+container.Name)
	}
	return ret
}

type ArtifactReportData struct {
	Complete  int
	Failed    int
	Missing   int
	Other     int
	Total     int
	Instances []*ReportInstanceData
}

type DependencyReportData struct {
	Complete     int
	Failed       int
	Contaminated int
	Building     int
	Other        int
	Total        int
	Instances    []*ReportInstanceData
}
type ReportData struct {
	Artifact   ArtifactReportData
	Dependency DependencyReportData
}

type ReportInstanceData struct {
	Name  string
	Logs  []string
	State string
	Yaml  string
}

type SortableArtifact []*ReportInstanceData

func (a SortableArtifact) Len() int           { return len(a) }
func (a SortableArtifact) Less(i, j int) bool { return strings.Compare(a[i].Name, a[j].Name) < 0 }
func (a SortableArtifact) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }

func setupMinikube(t *testing.T, ta *testArgs) *testArgs {

	ta = commonSetup(t, ta, minikubeGitCloneTaskUrl)

	//create the ServiceAccount
	sa := corev1.ServiceAccount{}
	sa.Name = "pipeline"
	sa.Namespace = ta.ns
	_, err := kubeClient.CoreV1().ServiceAccounts(ta.ns).Create(context.Background(), &sa, metav1.CreateOptions{})
	if err != nil {
		debugAndFailTest(ta, "pipeline SA not created in timely fashion")
	}
	//now create the binding
	crb := v1.ClusterRoleBinding{}
	crb.Name = "pipeline-" + ta.ns
	crb.Namespace = ta.ns
	crb.RoleRef.Name = "pipeline"
	crb.RoleRef.Kind = "ClusterRole"
	crb.Subjects = []v1.Subject{{Name: "pipeline", Kind: "ServiceAccount", Namespace: ta.ns}}
	_, err = kubeClient.RbacV1().ClusterRoleBindings().Create(context.Background(), &crb, metav1.CreateOptions{})
	if err != nil {
		debugAndFailTest(ta, "pipeline SA not created in timely fashion")
	}
	deployDockerRegistry(ta)

	owner := "testuser"
	jbsConfig := v1alpha1.JBSConfig{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:   ta.ns,
			Name:        v1alpha1.JBSConfigName,
			Annotations: map[string]string{jbsconfig.TestRegistry: "true"},
		},
		Spec: v1alpha1.JBSConfigSpec{
			EnableRebuilds: true,
			MavenBaseLocations: map[string]string{
				"maven-repository-300-jboss":     "https://repository.jboss.org/nexus/content/groups/public/",
				"maven-repository-301-confluent": "https://packages.confluent.io/maven",
				"maven-repository-302-redhat":    "https://maven.repository.redhat.com/ga",
				"maven-repository-303-jitpack":   "https://jitpack.io"},

			CacheSettings: v1alpha1.CacheSettings{ //up the cache size, this is a lot of builds all at once, we could limit the number of pods instead but this gets the test done faster
				RequestMemory: "1024Mi",
				LimitMemory:   "1024Mi",
				RequestCPU:    "200m",
				LimitCPU:      "500m",
				WorkerThreads: "100",
				DisableTLS:    true,
			},
			ImageRegistry: v1alpha1.ImageRegistry{
				Host:       "registry." + ta.ns + ".svc.cluster.local",
				Owner:      owner,
				Repository: "test-images",
				Port:       "80",
				Insecure:   true,
				PrependTag: strconv.FormatInt(time.Now().UnixMilli(), 10),
			},
		},
		Status: v1alpha1.JBSConfigStatus{},
	}
	_, err = jvmClient.JvmbuildserviceV1alpha1().JBSConfigs(ta.ns).Create(context.TODO(), &jbsConfig, metav1.CreateOptions{})
	if err != nil {
		debugAndFailTest(ta, err.Error())
	}

	time.Sleep(time.Second * 10)

	list, err := kubeClient.CoreV1().Namespaces().List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		debugAndFailTest(ta, err.Error())
	}
	for _, ns := range list.Items {
		podList, err := kubeClient.CoreV1().Pods(ns.Name).List(context.TODO(), metav1.ListOptions{})
		if err != nil {
			debugAndFailTest(ta, err.Error())
		}
		for _, pod := range podList.Items {
			fmt.Printf("Pod %s\n", pod.Name)
			for _, cs := range pod.Spec.Containers {
				fmt.Printf("Container %s has limit %s and request %s\n", cs.Name, cs.Resources.Limits.Cpu().String(), cs.Resources.Requests.Cpu().String())
			}
		}
	}

	err = waitForCache(ta)
	if err != nil {
		debugAndFailTest(ta, err.Error())
	}
	return ta
}

func deployDockerRegistry(ta *testArgs) {

	var one int32 = 1
	deployment := v13.Deployment{}
	deployment.Name = "registry"
	deployment.Namespace = ta.ns
	deployment.Spec.Replicas = &one
	deployment.Spec.Selector = &metav1.LabelSelector{MatchLabels: map[string]string{"app": "registry"}}
	deployment.Spec.Template = corev1.PodTemplateSpec{}
	deployment.Spec.Template.Labels = map[string]string{"app": "registry"}
	deployment.Spec.Template.Spec.Containers = []corev1.Container{
		{
			Name:  "registry",
			Image: "registry:2",
			Ports: []corev1.ContainerPort{{
				Name:          "http",
				ContainerPort: 5000,
			}},
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{"memory": resource.MustParse("50Mi"), "cpu": resource.MustParse("10m")},
				Limits:   corev1.ResourceList{"memory": resource.MustParse("50Mi"), "cpu": resource.MustParse("10m")},
			},
		},
	}
	_, err := kubeClient.AppsV1().Deployments(ta.ns).Create(context.Background(), &deployment, metav1.CreateOptions{})
	if err != nil {
		panic(err)
	}

	//and setup the service

	service := corev1.Service{
		ObjectMeta: ctrl.ObjectMeta{
			Name:      "registry",
			Namespace: ta.ns,
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{
					Name:       "http",
					Port:       80,
					TargetPort: intstr.IntOrString{IntVal: 5000},
				},
			},
			Type:     corev1.ServiceTypeNodePort,
			Selector: map[string]string{"app": "registry"},
		},
	}
	_, err = kubeClient.CoreV1().Services(ta.ns).Create(context.Background(), &service, metav1.CreateOptions{})
	if err != nil {
		panic(err)
	}
}

func watchEvents(eventClient ev1.EventInterface, ta *testArgs) {
	ctx := context.TODO()
	watch, err := eventClient.Watch(ctx, metav1.ListOptions{})
	if err != nil {
		panic(err)
	}
	for {
		res := <-watch.ResultChan()
		if res.Object == nil {
			continue
		}
		event, ok := res.Object.(*ev12.Event)
		if !ok {
			continue
		}
		if event.Type == corev1.EventTypeNormal {
			continue
		}
		ta.Logf(fmt.Sprintf("non-normal event reason %s about obj %s:%s message %s", event.Reason, event.Regarding.Kind, event.Regarding.Name, event.Note))
		dumpNodes(ta)
	}
}

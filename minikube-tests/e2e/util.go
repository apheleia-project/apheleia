package e2e

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/base64"
	"errors"
	"fmt"
	"html/template"
	"io"
	v13 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
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

const ServiceRegistryAuth = "testuser:$2y$05$Srpa4/8QOg4V/S6hHqcRF.XPTgCGzF5IOk9FQKvnfG2d3KRqJVikm"

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
	ta.Logf(fmt.Sprintf("dumpNodes found %d nodes in list, but only logging worker nodes", len(nodeList.Items)))
	for _, node := range nodeList.Items {
		_, ok := node.Labels["node-role.kubernetes.io/master"]
		if ok {
			continue
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

func setup(t *testing.T, ta *testArgs) *testArgs {
	if ta == nil {
		ta = &testArgs{
			t:        t,
			timeout:  time.Minute * 15,
			interval: time.Second * 15,
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
	}

	dumpNodes(ta)

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

	path, err := os.Getwd()
	if err != nil {
		debugAndFailTest(ta, err.Error())
	}

	ta.gitClone = &v1beta1.Task{}
	obj := streamRemoteYamlToTektonObj(gitCloneTaskUrl, ta.gitClone, ta)
	var ok bool
	ta.gitClone, ok = obj.(*v1beta1.Task)
	if !ok {
		debugAndFailTest(ta, fmt.Sprintf("%s did not produce a task: %#v", gitCloneTaskUrl, obj))
	}
	ta.gitClone, err = tektonClient.TektonV1beta1().Tasks(ta.ns).Create(context.TODO(), ta.gitClone, metav1.CreateOptions{})
	if err != nil {
		debugAndFailTest(ta, err.Error())
	}

	mavenYamlPath := filepath.Join(path, "..", "..", "hack", "example", "maven.yaml")
	ta.maven = &v1beta1.Task{}
	obj = streamFileYamlToTektonObj(mavenYamlPath, ta.maven, ta)
	ta.maven, ok = obj.(*v1beta1.Task)
	if !ok {
		debugAndFailTest(ta, fmt.Sprintf("%s did not produce a task: %#v", gitCloneTaskUrl, obj))
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
		debugAndFailTest(ta, fmt.Sprintf("%s did not produce a task: %#v", gitCloneTaskUrl, obj))
	}
	ta.pipeline, err = tektonClient.TektonV1beta1().Pipelines(ta.ns).Create(context.TODO(), ta.pipeline, metav1.CreateOptions{})
	if err != nil {
		debugAndFailTest(ta, err.Error())
	}
	runYamlPath := filepath.Join(path, "..", "..", "hack", "example", "run-e2e-shaded-app.yaml")
	ta.run = &v1beta1.PipelineRun{}
	obj = streamFileYamlToTektonObj(runYamlPath, ta.run, ta)
	ta.run, ok = obj.(*v1beta1.PipelineRun)
	if !ok {
		debugAndFailTest(ta, fmt.Sprintf("file %s did not produce a pipelinerun: %#v", runYamlPath, obj))
	}
	ta.run, err = tektonClient.TektonV1beta1().PipelineRuns(ta.ns).Create(context.TODO(), ta.run, metav1.CreateOptions{})
	if err != nil {
		debugAndFailTest(ta, err.Error())
	}

	cm := corev1.ConfigMap{}
	cm.Namespace = ta.ns
	cm.Name = "apheleia-config"
	cm.Data = map[string]string{"maven-repo": "https://todo.com", "aws-owner": "todo", "aws-domain": "todo"}
	_, err = kubeClient.CoreV1().ConfigMaps(ta.ns).Create(context.Background(), &cm, metav1.CreateOptions{})
	if err != nil {
		debugAndFailTest(ta, err.Error())
	}
	deployDockerRegistry(ta)

	owner := "testuser"
	jbsConfig := v1alpha1.JBSConfig{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: ta.ns,
			Name:      v1alpha1.JBSConfigName,
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
	secret := corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "jvm-build-image-secrets", Namespace: ta.ns},
		Data: map[string][]byte{".dockerconfigjson": []byte("{\"auths\":{\"registry." + ta.ns + ".svc.cluster.local\":{\"auth\": \"" + base64.StdEncoding.EncodeToString([]byte("testuser:testpassword")) + "\"}}}")}}
	_, err = kubeClient.CoreV1().Secrets(ta.ns).Create(context.TODO(), &secret, metav1.CreateOptions{})
	if err != nil {
		debugAndFailTest(ta, err.Error())
	}
	err = wait.PollImmediate(1*time.Second, 1*time.Minute, func() (done bool, err error) {
		_, err = kubeClient.AppsV1().Deployments(ta.ns).Get(context.TODO(), v1alpha1.CacheDeploymentName, metav1.GetOptions{})
		if err != nil {
			ta.Logf(fmt.Sprintf("get of cache: %s", err.Error()))
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		debugAndFailTest(ta, "cache not present in timely fashion")
	}
	return ta
}

func deployDockerRegistry(ta *testArgs) {

	cm := corev1.ConfigMap{}
	cm.Name = "registry-auth"
	cm.Namespace = ta.ns
	cm.Data = map[string]string{"htpasswd": ServiceRegistryAuth + "\n"}
	kubeClient.CoreV1().ConfigMaps(ta.ns).Create(context.Background(), &cm, metav1.CreateOptions{})

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
			Env: []corev1.EnvVar{
				{
					Name:  "REGISTRY_AUTH_HTPASSWD_PATH",
					Value: "/auth/htpasswd",
				}, {
					Name:  "REGISTRY_AUTH",
					Value: "htpasswd",
				}, {
					Name:  "REGISTRY_AUTH_HTPASSWD_REALM",
					Value: "Registry Realm",
				},
			},
			VolumeMounts: []corev1.VolumeMount{{
				Name:      "auth",
				MountPath: "/auth",
			}},
		},
	}
	deployment.Spec.Template.Spec.Volumes = []corev1.Volume{{
		Name:         "auth",
		VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "registry-auth"}}},
	}}
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

func bothCbsABsAndDBsGenerated(ta *testArgs) (bool, error) {
	cbList, err := apheleiaClient.ApheleiaV1alpha1().ComponentBuilds(ta.ns).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		ta.Logf(fmt.Sprintf("error listing componentbuilds: %s", err.Error()))
		return false, nil
	}
	gotCBs := false
	if len(cbList.Items) > 0 {
		gotCBs = true
	}
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
	if gotABs && gotDBs && gotCBs {
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
		//if db.Status.CurrentBuildRecipe != nil {
		//	tool = db.Status.CurrentBuildRecipe.Tool
		//}
		//if db.Status.FailedVerification {
		//	tool += " (FAILED VERIFICATION)"
		//}
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
		//for index, docker := range db.Status.DiagnosticDockerFiles {
		//
		//	localPart := localDir + "-docker-" + strconv.Itoa(index) + ".txt"
		//	fileName := directory + "/" + localPart
		//	err = os.WriteFile(fileName, []byte(docker), 0644) //#nosec G306
		//	if err != nil {
		//		print(fmt.Sprintf("Failed to write docker filer %s: %s", fileName, err))
		//	} else {
		//		instance.Logs = append(instance.Logs, localPart)
		//	}
		//}
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

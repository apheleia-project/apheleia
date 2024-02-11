//go:build normal
// +build normal

package e2e

import (
    "context"
    "encoding/json"
    "fmt"
    "github.com/redhat-appstudio/jvm-build-service/pkg/apis/jvmbuildservice/v1alpha1"
    "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1beta1"
    metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
    "k8s.io/apimachinery/pkg/util/wait"
    "knative.dev/pkg/apis"
    "os"
    "path/filepath"
    "strings"
    "testing"
)

func TestExampleRun(t *testing.T) {

    ta := setupMinikube(t, nil)

    defer GenerateStatusReport(ta.ns, jvmClient, kubeClient, tektonClient)
    //TODO, for now at least, keeping our test project to allow for analyzing the various CRD instances both for failure
    // and successful runs (in case a run succeeds, but we find something amiss if we look at passing runs; our in repo
    // tests do now run in conjunction with say the full suite of e2e's in the e2e-tests runs, so no contention there.
    //defer projectCleanup(ta)

    path, err := os.Getwd()
    if err != nil {
        debugAndFailTest(ta, err.Error())
    }
    ta.Logf(fmt.Sprintf("current working dir: %s", path))

    runYamlPath := filepath.Join(path, "..", "..", "hack", "example", "run-e2e-shaded-app.yaml")
    ta.run = &v1beta1.PipelineRun{}
    var ok bool
    obj := streamFileYamlToTektonObj(runYamlPath, ta.run, ta)
    ta.run, ok = obj.(*v1beta1.PipelineRun)
    if !ok {
        debugAndFailTest(ta, fmt.Sprintf("file %s did not produce a pipelinerun: %#v", runYamlPath, obj))
    }

    ta.run, err = tektonClient.TektonV1beta1().PipelineRuns(ta.ns).Create(context.TODO(), ta.run, metav1.CreateOptions{})
    if err != nil {
        debugAndFailTest(ta, err.Error())
    }

    ta.t.Run("pipelinerun completes with failure due to community dependencies", func(t *testing.T) {
        err = wait.PollImmediate(ta.interval, ta.timeout, func() (done bool, err error) {
            pr, err := tektonClient.TektonV1beta1().PipelineRuns(ta.ns).Get(context.TODO(), ta.run.Name, metav1.GetOptions{})
            if err != nil {
                ta.Logf(fmt.Sprintf("get pr %s produced err: %s", ta.run.Name, err.Error()))
                return false, nil
            }
            if !pr.IsDone() {
                if err != nil {
                    ta.Logf(fmt.Sprintf("problem marshalling in progress pipelinerun to bytes: %s", err.Error()))
                    return false, nil
                }
                ta.Logf(fmt.Sprintf("in flight pipeline run: %s", pr.Name))
                return false, nil
            }
            if pr.GetStatusCondition().GetCondition(apis.ConditionSucceeded).IsTrue() {
                prBytes, err := json.MarshalIndent(pr, "", "  ")
                if err != nil {
                    ta.Logf(fmt.Sprintf("problem marshalling failed pipelinerun to bytes: %s", err.Error()))
                    return false, nil
                }
                debugAndFailTest(ta, fmt.Sprintf("PipelineRun should not have passed: %s", string(prBytes)))
            }
            return true, nil
        })
        if err != nil {
            debugAndFailTest(ta, fmt.Sprintf("failure occured when waiting for the pipeline run to complete: %v", err))
        }
    })

    ta.t.Run("componentbuilds, artifactbuilds and dependencybuilds generated", func(t *testing.T) {
        err = wait.PollImmediate(ta.interval, ta.timeout, func() (done bool, err error) {
            return bothABsAndDBsGenerated(ta)
        })
        if err != nil {
            debugAndFailTest(ta, "timed out waiting for generation of artifactbuilds and dependencybuilds")
        }
    })

    ta.t.Run("all artfactbuilds and dependencybuilds complete", func(t *testing.T) {
        err = wait.PollImmediate(ta.interval, 2*ta.timeout, func() (done bool, err error) {
            abList, err := jvmClient.JvmbuildserviceV1alpha1().ArtifactBuilds(ta.ns).List(context.TODO(), metav1.ListOptions{})
            if err != nil {
                ta.Logf(fmt.Sprintf("error list artifactbuilds: %s", err.Error()))
                return false, nil
            }
            //we want to make sure there is more than one ab, and that they are all complete
            abComplete := len(abList.Items) > 0
            ta.Logf(fmt.Sprintf("number of artifactbuilds: %d", len(abList.Items)))
            for _, ab := range abList.Items {
                if ab.Status.State != v1alpha1.ArtifactBuildStateComplete {
                    ta.Logf(fmt.Sprintf("artifactbuild %s not complete", ab.Spec.GAV))
                    abComplete = false
                    break
                }
            }
            dbList, err := jvmClient.JvmbuildserviceV1alpha1().DependencyBuilds(ta.ns).List(context.TODO(), metav1.ListOptions{})
            if err != nil {
                ta.Logf(fmt.Sprintf("error list dependencybuilds: %s", err.Error()))
                return false, nil
            }
            dbComplete := len(dbList.Items) > 0
            ta.Logf(fmt.Sprintf("number of dependencybuilds: %d", len(dbList.Items)))
            for _, db := range dbList.Items {
                if db.Status.State != v1alpha1.DependencyBuildStateComplete {
                    ta.Logf(fmt.Sprintf("depedencybuild %s not complete", db.Spec.ScmInfo.SCMURL))
                    dbComplete = false
                    break
                } else if db.Status.State == v1alpha1.DependencyBuildStateFailed {
                    ta.Logf(fmt.Sprintf("depedencybuild %s FAILED", db.Spec.ScmInfo.SCMURL))
                    return false, fmt.Errorf("depedencybuild %s for repo %s FAILED", db.Name, db.Spec.ScmInfo.SCMURL)
                }
            }
            if abComplete && dbComplete {
                return true, nil
            }
            return false, nil
        })
        if err != nil {
            debugAndFailTest(ta, "timed out waiting for some artifactbuilds and dependencybuilds to complete")
        }
    })

    ta.t.Run("contaminated build is resolved", func(t *testing.T) {
        //our sample repo has shaded-jdk11 which is contaminated by simple-jdk8
        var contaminated string
        var simpleJDK8 string
        err = wait.PollImmediate(ta.interval, 2*ta.timeout, func() (done bool, err error) {

            dbContaminated := false
            shadedComplete := false
            contaminantBuild := false
            dbList, err := jvmClient.JvmbuildserviceV1alpha1().DependencyBuilds(ta.ns).List(context.TODO(), metav1.ListOptions{})
            if err != nil {
                ta.Logf(fmt.Sprintf("error list dependencybuilds: %s", err.Error()))
                return false, err
            }
            ta.Logf(fmt.Sprintf("number of dependencybuilds: %d", len(dbList.Items)))
            for _, db := range dbList.Items {
                if db.Status.State == v1alpha1.DependencyBuildStateContaminated {
                    dbContaminated = true
                    contaminated = db.Name
                    break
                } else if strings.Contains(db.Spec.ScmInfo.SCMURL, "shaded-jdk11") && db.Status.State == v1alpha1.DependencyBuildStateComplete {
                    //its also possible that the build has already resolved itself
                    contaminated = db.Name
                    shadedComplete = true
                } else if strings.Contains(db.Spec.ScmInfo.SCMURL, "simple-jdk8") {
                    contaminantBuild = true
                }
            }
            if dbContaminated || (shadedComplete && contaminantBuild) {
                return true, nil
            }
            return false, nil
        })
        if err != nil {
            debugAndFailTest(ta, "timed out waiting for contaminated build to appear")
        }
        ta.Logf(fmt.Sprintf("contaminated dependencybuild: %s", contaminated))
        //make sure simple-jdk8 was requested as a result
        err = wait.PollImmediate(ta.interval, 2*ta.timeout, func() (done bool, err error) {
            abList, err := jvmClient.JvmbuildserviceV1alpha1().ArtifactBuilds(ta.ns).List(context.TODO(), metav1.ListOptions{})
            if err != nil {
                ta.Logf(fmt.Sprintf("error list artifactbuilds: %s", err.Error()))
                return false, err
            }
            found := false
            ta.Logf(fmt.Sprintf("number of artifactbuilds: %d", len(abList.Items)))
            for _, ab := range abList.Items {
                if strings.Contains(ab.Spec.GAV, "simple-jdk8") {
                    simpleJDK8 = ab.Name
                    found = true
                    break
                }
            }
            return found, nil
        })
        if err != nil {
            debugAndFailTest(ta, "timed out waiting for simple-jdk8 to appear as an artifactbuild")
        }
        //now make sure simple-jdk8 eventually completes
        err = wait.PollImmediate(ta.interval, 2*ta.timeout, func() (done bool, err error) {
            ab, err := jvmClient.JvmbuildserviceV1alpha1().ArtifactBuilds(ta.ns).Get(context.TODO(), simpleJDK8, metav1.GetOptions{})
            if err != nil {
                ta.Logf(fmt.Sprintf("error getting simple-jdk8 ArtifactBuild: %s", err.Error()))
                return false, err
            }
            ta.Logf(fmt.Sprintf("simple-jdk8 State: %s", ab.Status.State))
            return ab.Status.State == v1alpha1.ArtifactBuildStateComplete, nil
        })
        if err != nil {
            debugAndFailTest(ta, "timed out waiting for simple-jdk8 to complete")
        }
        //now make sure shaded-jdk11 eventually completes
        err = wait.PollImmediate(ta.interval, 2*ta.timeout, func() (done bool, err error) {
            db, err := jvmClient.JvmbuildserviceV1alpha1().DependencyBuilds(ta.ns).Get(context.TODO(), contaminated, metav1.GetOptions{})
            if err != nil {
                ta.Logf(fmt.Sprintf("error getting shaded-jdk11 DependencyBuild: %s", err.Error()))
                return false, err
            }
            ta.Logf(fmt.Sprintf("shaded-jdk11 State: %s", db.Status.State))
            if db.Status.State == v1alpha1.DependencyBuildStateFailed {
                msg := fmt.Sprintf("contaminated db %s failed, exitting wait", contaminated)
                ta.Logf(msg)
                return false, fmt.Errorf(msg)
            }
            return db.Status.State == v1alpha1.DependencyBuildStateComplete, err
        })
        if err != nil {
            debugAndFailTest(ta, "timed out waiting for shaded-jdk11 to complete")
        }
    })

    ta.t.Run("make sure second build access cached dependencies", func(t *testing.T) {
        ta.run, err = tektonClient.TektonV1beta1().PipelineRuns(ta.ns).Create(context.TODO(), ta.run, metav1.CreateOptions{})
        if err != nil {
            debugAndFailTest(ta, err.Error())
        }

        err = wait.PollImmediate(ta.interval, ta.timeout, func() (done bool, err error) {
            pr, err := tektonClient.TektonV1beta1().PipelineRuns(ta.ns).Get(context.TODO(), ta.run.Name, metav1.GetOptions{})
            if err != nil {
                ta.Logf(fmt.Sprintf("get pr %s produced err: %s", ta.run.Name, err.Error()))
                return false, nil
            }
            if !pr.IsDone() {
                if err != nil {
                    ta.Logf(fmt.Sprintf("problem marshalling in progress pipelinerun to bytes: %s", err.Error()))
                    return false, nil
                }
                ta.Logf(fmt.Sprintf("in flight pipeline run: %s", pr.Name))
                return false, nil
            }
            if !pr.GetStatusCondition().GetCondition(apis.ConditionSucceeded).IsTrue() {
                prBytes, err := json.MarshalIndent(pr, "", "  ")
                if err != nil {
                    ta.Logf(fmt.Sprintf("problem marshalling failed pipelinerun to bytes: %s", err.Error()))
                    return false, nil
                }
                debugAndFailTest(ta, fmt.Sprintf("PipelineRun should have passed: %s", string(prBytes)))
            }
            return true, nil
        })
        if err != nil {
            debugAndFailTest(ta, fmt.Sprintf("failure occured when waiting for the pipeline run to complete: %v", err))
        }
    })

}

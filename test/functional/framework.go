package functional

import (
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v2"

	"github.com/ViaQ/logerr/log"
	"github.com/openshift/cluster-logging-operator/internal/pkg/generator/forwarder"
	logging "github.com/openshift/cluster-logging-operator/pkg/apis/logging/v1"
	"github.com/openshift/cluster-logging-operator/pkg/certificates"
	"github.com/openshift/cluster-logging-operator/pkg/constants"
	"github.com/openshift/cluster-logging-operator/pkg/utils"
	"github.com/openshift/cluster-logging-operator/test/client"
	"github.com/openshift/cluster-logging-operator/test/helpers/oc"
	"github.com/openshift/cluster-logging-operator/test/runtime"
	corev1 "k8s.io/api/core/v1"
)

//FluentddFunctionalFramework deploys stand alone fluentd with the fluent.conf as generated by input ClusterLogForwarder CR
type FluentdFunctionalFramework struct {
	Name      string
	Namespace string
	Conf      string
	image     string
	labels    map[string]string
	Forwarder *logging.ClusterLogForwarder
	test      *client.Test
	pod       *corev1.Pod
}

func NewFluentdFunctionalFramework() *FluentdFunctionalFramework {
	verbosity := uint8(9)
	if level, found := os.LookupEnv("LOG_LEVEL"); found {
		if i, err := strconv.Atoi(level); err == nil {
			verbosity = uint8(i)
		}
	}

	opt := log.WithVerbosity(verbosity)
	log.MustInitWithOptions("fluent-ftf", []log.Option{opt})
	t := client.NewTest()
	testName := fmt.Sprintf("test-fluent-%d", rand.Intn(1000))
	framework := &FluentdFunctionalFramework{
		Name:      testName,
		Namespace: t.NS.Name,
		image:     utils.GetComponentImage(constants.FluentdName),
		labels: map[string]string{
			"testtype": "functional",
			"testname": testName,
		},
		test:      t,
		Forwarder: runtime.NewClusterLogForwarder(),
	}
	return framework
}

func (f *FluentdFunctionalFramework) Cleanup() {
	f.test.Close()
}

func (f *FluentdFunctionalFramework) RunCommand(cmdString string) (string, error) {
	cmd := strings.Split(cmdString, " ")
	log.V(2).Info("Running", "cmd", cmdString)
	out, err := runtime.Exec(f.pod, cmd[0], cmd[1:]...).CombinedOutput()
	return string(out), err
}

//Deploy the objects needed to functional test
func (f *FluentdFunctionalFramework) Deploy() (err error) {
	log.V(2).Info("Generating config", "forwarder", f.Forwarder)
	yaml, _ := yaml.Marshal(f.Forwarder)
	if f.Conf, err = forwarder.Generate(string(yaml), false); err != nil {
		return err
	}
	log.V(2).Info("Generating Certificates")
	if err = certificates.GenerateCertificates(f.test.NS.Name,
		utils.GetScriptsDir(), "elasticsearch",
		utils.DefaultWorkingDir); err != nil {
		return err
	}
	log.V(2).Info("Creating config configmap")
	configmap := runtime.NewConfigMap(f.test.NS.Name, f.Name, map[string]string{})
	runtime.NewConfigMapBuilder(configmap).
		Add("fluent.conf", f.Conf).
		Add("run.sh", string(utils.GetFileContents(utils.GetShareDir()+"/fluentd/run.sh")))
	if err = f.test.Client.Create(configmap); err != nil {
		return err
	}

	log.V(2).Info("Creating certs configmap")
	certsName := "certs-" + f.Name
	certs := runtime.NewConfigMap(f.test.NS.Name, certsName, map[string]string{})
	runtime.NewConfigMapBuilder(certs).
		Add("tls.key", string(utils.GetWorkingDirFileContents("system.logging.fluentd.key"))).
		Add("tls.crt", string(utils.GetWorkingDirFileContents("system.logging.fluentd.crt")))
	if err = f.test.Client.Create(certs); err != nil {
		return err
	}

	log.V(2).Info("Creating service")
	service := runtime.NewService(f.test.NS.Name, f.Name)
	runtime.NewServiceBuilder(service).
		AddServicePort(24231, 24231).
		WithSelector(f.labels)
	if err = f.test.Client.Create(service); err != nil {
		return err
	}

	log.V(2).Info("Creating pod")
	containers := []corev1.Container{}
	f.pod = runtime.NewPod(f.test.NS.Name, f.Name, containers...)
	runtime.NewPodBuilder(f.pod).
		WithLabels(f.labels).
		AddConfigMapVolume("config", f.Name).
		AddConfigMapVolume("entrypoint", f.Name).
		AddConfigMapVolume("certs", certsName).
		AddContainer(f.Name, f.image).
		AddEnvVar("LOG_LEVEL", "debug").
		AddEnvVarFromFieldRef("POD_IP", "status.podIP").
		AddVolumeMount("config", "/etc/fluent/configs.d/user", "", true).
		AddVolumeMount("entrypoint", "/opt/app-root/src/run.sh", "run.sh", true).
		AddVolumeMount("certs", "/etc/fluent/metrics", "", true).
		End()
	if err = f.test.Client.Create(f.pod); err != nil {
		return err
	}

	log.V(2).Info("waiting for pod to be ready")
	if err = oc.Literal().From(fmt.Sprintf("oc wait -n %s pod/%s --timeout=60s --for=condition=Ready", f.test.NS.Name, f.Name)).Output(); err != nil {
		return err
	}
	return nil
}

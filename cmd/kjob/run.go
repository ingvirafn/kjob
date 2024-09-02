package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/ingvirafn/kjob/pkg/jobrunner"
	"github.com/spf13/cobra"
	"k8s.io/client-go/kubernetes"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	"k8s.io/client-go/tools/clientcmd"
)

var runJobCmd = &cobra.Command{
	Use:     `run -t cron-job-template -n namespace`,
	Example: `  run --template curl --command "curl -sL flagger.app/index.yaml" --cleanup=false --timeout=2m`,
	RunE:    runJob,
}

var (
	kubeconfig string
	template   string
	namespace  string
	command    string

	//
	crd        bool
	group      string
	apiVersion string
	kind       string
	pjs        bool

	jin string

	cleanup    bool
	timeout    time.Duration
	ttl        int32
	shell      string
	envs       []string
	pullAlways bool
)

func init() {
	if home := homeDir(); home != "" {
		runJobCmd.Flags().StringVarP(&kubeconfig, "kubeconfig", "", filepath.Join(home, ".kube", "config"), "path to the kubeconfig file")
	} else {
		runJobCmd.Flags().StringVarP(&kubeconfig, "kubeconfig", "", "", "absolute path to the kubeconfig file")
	}
	runJobCmd.Flags().StringVarP(&template, "template", "t", "", "cron job / job template name")
	runJobCmd.Flags().StringVarP(&namespace, "namespace", "n", "default", "namespace of the cron job template")
	runJobCmd.Flags().StringVarP(&command, "command", "c", "", "override job command")
	runJobCmd.Flags().StringVarP(&shell, "shell", "", "sh", "command shell")

	// CRD flags
	runJobCmd.Flags().StringVarP(&group, "group", "", "", "Group of the CRD object")
	runJobCmd.Flags().StringVarP(&apiVersion, "apiVersion", "", "", "API version of the CRD object")
	runJobCmd.Flags().StringVarP(&kind, "kind", "", "", "Kind of the CRD object")
	runJobCmd.Flags().BoolVarP(&crd, "crd", "", false, "use CustomResourceDefinition")

	runJobCmd.Flags().StringVarP(&jin, "jin", "", "", "Job instance name")
	runJobCmd.Flags().BoolVarP(&pjs, "printjobspec", "", false, "Print job spec before posting job")

	runJobCmd.Flags().BoolVarP(&cleanup, "cleanup", "", false, "delete job and pods after completion")
	runJobCmd.Flags().BoolVarP(&pullAlways, "pullalways", "a", true, "configure the container spec to \"PullAlways\" the image")
	runJobCmd.Flags().DurationVarP(&timeout, "timeout", "", time.Minute, "timeout for Kubernetes operations")
	runJobCmd.Flags().Int32Var(&ttl, "ttl", int32(0), "ttl seconds after finished (how long the job stays in kube until it is deleted)")

	runJobCmd.Flags().StringSliceVarP(&envs, "env", "e", []string{}, "environment variables to forward from the executing shell to the containers")

	rootCmd.AddCommand(runJobCmd)
}

func runJob(cmd *cobra.Command, args []string) error {
	if template == "" {
		return fmt.Errorf("--template is required")
	}
	if namespace == "" {
		return fmt.Errorf("--namespace is required")
	}
	if crd && (group == "" || apiVersion == "" || kind == "") {
		return fmt.Errorf("--group, --apiVersion and --kind are required for CRD")
	}

	stopCh := setupSignalHandler()

	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		log.Fatalf("Error building kubeconfig: %v", err)
	}

	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Fatalf("Error building kubernetes client: %v", err)
	}

	ctrl, err := jobrunner.NewJobController(config, client, namespace, stopCh)
	if err != nil {
		log.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	job := jobrunner.Job{
		TemplateRef: jobrunner.JobTemplateRef{
			Name:       template,
			Namespace:  namespace,
			Group:      group,
			APIVersion: apiVersion,
			Kind:       kind,
			Crd:        crd,
		},
		BackoffLimit:            0,
		Timeout:                 timeout,
		Command:                 command,
		CommandShell:            shell,
		PrintJobSpec:            pjs,
		Envs:                    envs,
		PullAlways:              pullAlways,
		JobInstanceName:         jin,
		TtlSecondsAfterFinished: ttl,
	}

	result, err := ctrl.Run(ctx, job, cleanup)
	if result != nil {
		if result.Output != "" {
			log.Print(result.Output)
		}
		if result.Status != nil && result.Status.Failed {
			log.Fatalf("error: %s", result.Status.Message)
		} else {
			log.Printf("> Job %s/%s succeeded", result.Namespace, result.Name)
		}
	}
	if err != nil {
		log.Fatalf("error: %v", err)
	}

	return nil
}

func setupSignalHandler() <-chan struct{} {
	stop := make(chan struct{})
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-c
		close(stop)
		os.Exit(1)
	}()

	return stop
}

func homeDir() string {
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	return os.Getenv("USERPROFILE") // windows
}

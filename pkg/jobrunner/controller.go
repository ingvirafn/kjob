package jobrunner

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	kubeInformers "k8s.io/client-go/informers"
	cronJobInformers "k8s.io/client-go/informers/batch/v1"
	jobInformers "k8s.io/client-go/informers/batch/v1"
	podInformers "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
)

type JobController struct {
	client          *kubernetes.Clientset
	cronJobInformer cronJobInformers.CronJobInformer
	jobInformer     jobInformers.JobInformer
	podInformer     podInformers.PodInformer
	stopChan        <-chan struct{}
}

// NewJobController starts Kubernetes informers for the specified namespace and returns a job controller.
func NewJobController(client *kubernetes.Clientset, namespace string, stopChan <-chan struct{}) (*JobController, error) {
	factory := kubeInformers.NewSharedInformerFactoryWithOptions(client, 5*time.Second, kubeInformers.WithNamespace(namespace))
	timeoutError := "error: failed to wait for %s cache to sync"

	if _, err := client.BatchV1().CronJobs(namespace).List(context.TODO(), metav1.ListOptions{Limit: 1}); err != nil {
		return nil, fmt.Errorf("error: can't list cron jobs %w", err)
	}
	cronJobsInformer := factory.Batch().V1().CronJobs()
	go cronJobsInformer.Informer().Run(stopChan)
	if ok := cache.WaitForCacheSync(stopChan, cronJobsInformer.Informer().HasSynced); !ok {
		return nil, fmt.Errorf(timeoutError, "CronJobs")
	}

	if _, err := client.BatchV1().Jobs(namespace).List(context.TODO(), metav1.ListOptions{Limit: 1}); err != nil {
		return nil, fmt.Errorf("error: can't list jobs %w", err)
	}
	jobsInformer := factory.Batch().V1().Jobs()
	go jobsInformer.Informer().Run(stopChan)
	if ok := cache.WaitForCacheSync(stopChan, jobsInformer.Informer().HasSynced); !ok {
		return nil, fmt.Errorf(timeoutError, "Jobs")
	}

	if _, err := client.CoreV1().Pods(namespace).List(context.TODO(), metav1.ListOptions{Limit: 1}); err != nil {
		return nil, fmt.Errorf("error: can't list pods %w", err)
	}
	podsInformer := factory.Core().V1().Pods()
	go podsInformer.Informer().Run(stopChan)
	if ok := cache.WaitForCacheSync(stopChan, podsInformer.Informer().HasSynced); !ok {
		return nil, fmt.Errorf(timeoutError, "Pods")
	}

	return &JobController{
		client:          client,
		cronJobInformer: cronJobsInformer,
		jobInformer:     jobsInformer,
		podInformer:     podsInformer,
		stopChan:        stopChan,
	}, nil
}

func (ctrl *JobController) Run(ctx context.Context, task Job, cleanup bool) (*JobResult, error) {
	suspendedjob, err := ctrl.jobInformer.Lister().Jobs(task.TemplateRef.Namespace).Get(task.TemplateRef.Name)
	var j batchv1.JobSpec

	if err == nil {
		j = suspendedjob.Spec
		j.Template.Labels = nil //make(map[string]string)
		j.Selector = nil
		sus := false
		j.Suspend = &sus
	} else {
		cronjob, err := ctrl.cronJobInformer.Lister().CronJobs(task.TemplateRef.Namespace).Get(task.TemplateRef.Name)
		if err != nil {
			return nil, fmt.Errorf("error: %s.%s get failed: %w",
				task.TemplateRef.Name, task.TemplateRef.Namespace, err)
		}
		j = cronjob.Spec.JobTemplate.Spec
	}

	job, err := ctrl.createJob(ctx, task, j)
	if err != nil {
		return nil, fmt.Errorf("error: %s.%s create job failed: %w",
			task.TemplateRef.Name, task.TemplateRef.Namespace, err)
	}

	result := &JobResult{
		Name:      job.GetName(),
		Namespace: job.GetNamespace(),
		Status:    nil,
		Output:    "",
	}

	jobName := job.GetName()
	done := false
	for !done {
		for _, condition := range job.Status.Conditions {
			switch condition.Type {
			case batchv1.JobFailed:
				result.Status = &JobStatus{
					Failed:  true,
					Message: condition.Message,
				}
				done = true
			case batchv1.JobComplete:
				result.Status = &JobStatus{
					Failed:  false,
					Message: condition.Message,
				}
				done = true
			}
		}
		time.Sleep(1 * time.Second)
		job, err = ctrl.jobInformer.Lister().Jobs(task.TemplateRef.Namespace).Get(jobName)
		if err != nil {
			return nil, fmt.Errorf("error: %s.%s list job failed: %w",
				task.TemplateRef.Name, task.TemplateRef.Namespace, err)
		}
	}

	selector := fmt.Sprintf("job-name=%s", jobName)
	set, _ := labels.ConvertSelectorToLabelsMap(selector)

	jobPods, err := ctrl.podInformer.Lister().Pods(task.TemplateRef.Namespace).List(labels.SelectorFromSet(set))
	if err != nil {
		return nil, fmt.Errorf("error: %s.%s list pods failed: %w",
			task.TemplateRef.Name, task.TemplateRef.Namespace, err)
	}

	pods := make([]string, 0, len(jobPods))
	for _, pod := range jobPods {
		pods = append(pods, pod.GetName())
	}

	if len(pods) < 1 {
		return result, fmt.Errorf("error: no pods found for job %s.%s selector %s",
			jobName, task.TemplateRef.Namespace, selector)
	}

	result.Output, err = ctrl.logs(ctx, pods, task.TemplateRef.Namespace)
	if err != nil {
		return result, fmt.Errorf("error: %s.%s logs failed: %w",
			jobName, task.TemplateRef.Namespace, err)
	}

	if cleanup {
		err = ctrl.cleanup(ctx, pods, jobName, task.TemplateRef.Namespace)
		if err != nil {
			return result, fmt.Errorf("error: %s.%s cleanup failed: %w",
				jobName, task.TemplateRef.Namespace, err)
		}
	}

	return result, nil
}

func (ctrl *JobController) createJob(ctx context.Context, task Job, spec batchv1.JobSpec) (*batchv1.Job, error) {
	// override command
	if task.Command != "" {
		spec.Template.Spec.Containers[0].Command = []string{
			task.CommandShell,
			"-c",
			task.Command,
		}
	}
	// set environment variables (TODO: should not replace, should append)
	if len(task.Envs) != 0 {
		// log.Printf("Adding environment variables")
		newEnvs := make([]corev1.EnvVar, len(task.Envs))

		for i, ee := range task.Envs {
			// log.Printf("Adding environment variables: %s. Value: %s", ee, os.Getenv(ee))
			newEnvs[i] = corev1.EnvVar{Name: ee, Value: os.Getenv(ee)}
		}

		spec.Template.Spec.Containers[0].Env = newEnvs
	}

	// override backoff
	if spec.BackoffLimit == nil {
		spec.BackoffLimit = &task.BackoffLimit
	}

	if task.PullAlways {
		spec.Template.Spec.Containers[0].ImagePullPolicy = corev1.PullAlways
	}

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: task.TemplateRef.Name + "-",
			Namespace:    task.TemplateRef.Namespace,
		},
		Spec: spec,
	}

	return ctrl.client.BatchV1().Jobs(task.TemplateRef.Namespace).Create(ctx, job, metav1.CreateOptions{})
}

func (ctrl *JobController) logs(ctx context.Context, pods []string, namespace string) (string, error) {
	buf := new(bytes.Buffer)

	for _, pod := range pods {
		req := ctrl.client.CoreV1().Pods(namespace).GetLogs(pod, &corev1.PodLogOptions{})
		stream, err := req.Stream(ctx)
		if err != nil {
			return "", fmt.Errorf("error while reading %s logs %w", pod, err)
		}

		_, err = io.Copy(buf, stream)
		stream.Close()
		if err != nil {
			return "", fmt.Errorf("error while reading %s logs %w", pod, err)
		}
	}

	return buf.String(), nil
}

func (ctrl *JobController) cleanup(ctx context.Context, pods []string, job string, namespace string) error {
	err := ctrl.client.BatchV1().Jobs(namespace).Delete(ctx, job, metav1.DeleteOptions{})
	if err != nil && !errors.IsNotFound(err) {
		return err
	}

	for _, item := range pods {
		err = ctrl.client.CoreV1().Pods(namespace).Delete(ctx, item, metav1.DeleteOptions{})
		if err != nil && !errors.IsNotFound(err) {
			return err
		}
	}
	return nil
}

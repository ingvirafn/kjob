# kjob

[![e2e](https://github.com/ingvirafn/kjob/workflows/ci/badge.svg)](https://github.com/ingvirafn/kjob/actions)
[![release](https://github.com/ingvirafn/kjob/workflows/release/badge.svg)](https://github.com/ingvirafn/kjob/actions)

Fork of https://github.com/stefanprodan/kjob

**kjob** is a small utility written in Go that:
* creates a Kubernetes Job from a CronJob template
* overrides the job command if specified
* adds environment variables from host if specified
* waits for job completion
* prints the pods logs
* removes the pods and the job object
* exits with status 1 if the job failed

## Usage

Download kjob binary from GitHub [releases](https://github.com/ingvirafn/kjob/releases/latest).

Create a suspended CronJob that will serve as a template using [cronjob file](cronjob.yaml)

Run the job:
```bash
./kjob run --template curl-job --command "export" --env NAME --env PWD --cleanup=true --pullalways --timeout=2m
```


List of available flags:

```text
$ kjob run --help
Usage:
  kjob run -t cron-job-template -n namespace [flags]

Examples:
  run --template curl --command "curl -sL flagger.app/index.yaml" --cleanup=false --timeout=2m

Flags:
      --cleanup             delete job and pods after completion (default true)
  -c, --command string      override job command
  -e, --env strings         environment variables to forward from the executing shell to the containers
  -h, --help                help for run
      --kubeconfig string   path to the kubeconfig file (default "/home/ingvirafn/.kube/config")
  -n, --namespace string    namespace of the cron job template (default "default")
  -a, --pullalways          configure the container spec to "PullAlways" the image (default true)
      --shell string        command shell (default "sh")
  -t, --template string     cron job template name
      --timeout duration    timeout for Kubernetes operations (default 1m0s)

--template is required
```

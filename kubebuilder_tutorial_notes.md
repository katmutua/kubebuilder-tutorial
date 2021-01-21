### Notes following kubebuilder-book
*tldr on reconciliation*
It’s a controller’s job to ensure that, for any given object, the actual state of the world (both the cluster state, and potentially external state like running containers for Kubelet or loadbalancers for a cloud provider) matches the desired state in the object

In controller-runtime, the logic that implements the reconciling for a specific kind is called a Reconciler.

*code exercise*
create the project
```
mkdir $GOPATH/kubebuilder
cd $GOPATH/kubebuilder
```

run the init command
`kubebuilder init --domain tutorial.kubebuilder.io`

When we talk about APIs in Kubernetes, we often use 4 terms:
1. groups: simply a collection of related functionality
2. versions: each group has one or more versions, which, as the name suggests, allow us to change how an API works over time.
3. kinds: each API group-version contains one or more API types which we call kinds
4. resources:  a resource is simply a use of a Kind in the API

When we refer to a kind in a particular group-version, we’ll call it a GroupVersionKind, or GVK
The Scheme we saw before is simply a way to keep track of what Go type corresponds to a given GVK

create a new kind called CronJob
```
kubebuilder create api --group batch --version v1 --kind CronJob
```

Explaining the generated code (`v1/cronjobtypes.go` code).
- import the meta/v1 API group
- define types for the Spec and Status of our Kind.
  - Kubernetes functions by reconciling desired state (Spec) with actual cluster state
    (other objects’ Status) and external state, and then recording what it observed (Status)
  - every functional object includes spec and status
  - few types, like ConfigMap don’t follow this pattern, since they don’t encode desired
    state, but most types do.
- define the types corresponding to actual Kinds, CronJob and CronJobList
   - CronJob is our root type, and describes the CronJob kind; it also contains TypeMeta (which
     describes API version and Kind), and also contains ObjectMeta, which holds things like name,
     namespace, and labels
   - CronJobList is simply a container for multiple CronJobs: it’s the Kind used in bulk operations, like LIST
   - In general, we never modify either of these -- all modifications go in either Spec or Status

The code +kubebuilder:object:root comment is called a marker
`markers` act as extra metadata, telling controller-tools (our code and YAML generator) extra information.

This particular one tells the object generator that this type represents a Kind, the object generator generates an implementation of the runtime.Object interface for us, which is the standard interface that all types representing Kinds must implement

#TODO
- define a scheme

##### Designing your API
serialized fields must be camelCase
omitempty struct tag to mark that a field should be omitted from serialization when empty
Fields may use most of the primitive types
  - Numbers are the exception: for API compatibility only three forms of numbers are accepted:
    - int32 and int64 for integers, and resource.Quantity for decimals.
        - Quantities are a special notation for decimal numbers that have an explicitly fixed representation that makes them more portable across machines

- generate a new cronjob kind
  ```
  kubebuilder create api --group batch --version v1 --kind CronJob
  ```
- add this to your imports
  ```
  package v1

  import (
      batchv1beta1 "k8s.io/api/batch/v1beta1"
      corev1 "k8s.io/api/core/v1"
      metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
  )
  ```
- define the extra types needed in the cronjob types
  ```
  // CronJobSpec defines the desired state of CronJob
  type CronJobSpec struct {
      // +kubebuilder:validation:MinLength=0

      // The schedule in Cron format, see https://en.wikipedia.org/wiki/Cron.
      Schedule string `json:"schedule"`

      // +kubebuilder:validation:Minimum=0

      // Optional deadline in seconds for starting the job if it misses scheduled
      // time for any reason.  Missed jobs executions will be counted as failed ones.
      // +optional
      StartingDeadlineSeconds *int64 `json:"startingDeadlineSeconds,omitempty"`

      // Specifies how to treat concurrent executions of a Job.
      // Valid values are:
      // - "Allow" (default): allows CronJobs to run concurrently;
      // - "Forbid": forbids concurrent runs, skipping next run if previous run hasn't finished yet;
      // - "Replace": cancels currently running job and replaces it with a new one
      // +optional
      ConcurrencyPolicy ConcurrencyPolicy `json:"concurrencyPolicy,omitempty"`

      // This flag tells the controller to suspend subsequent executions, it does
      // not apply to already started executions.  Defaults to false.
      // +optional
      Suspend *bool `json:"suspend,omitempty"`

      // Specifies the job that will be created when executing a CronJob.
      JobTemplate batchv1beta1.JobTemplateSpec `json:"jobTemplate"`

      // +kubebuilder:validation:Minimum=0

      // The number of successful finished jobs to retain.
      // This is a pointer to distinguish between explicit zero and not specified.
      // +optional
      SuccessfulJobsHistoryLimit *int32 `json:"successfulJobsHistoryLimit,omitempty"`

      // +kubebuilder:validation:Minimum=0

      // The number of failed finished jobs to retain.
      // This is a pointer to distinguish between explicit zero and not specified.
      // +optional
      FailedJobsHistoryLimit *int32 `json:"failedJobsHistoryLimit,omitempty"`
  }
  ```

  - define a custom type to hold our ConcurencyPolicy
  ```
    / ConcurrencyPolicy describes how the job will be handled.
  // Only one of the following concurrent policies may be specified.
  // If none of the following policies is specified, the default one
  // is AllowConcurrent.
  // +kubebuilder:validation:Enum=Allow;Forbid;Replace
  type ConcurrencyPolicy string

  const (
      // AllowConcurrent allows CronJobs to run concurrently.
      AllowConcurrent ConcurrencyPolicy = "Allow"

      // ForbidConcurrent forbids concurrent runs, skipping next run if previous
      // hasn't finished yet.
      ForbidConcurrent ConcurrencyPolicy = "Forbid"

      // ReplaceConcurrent cancels currently running job and replaces it with a new one.
      ReplaceConcurrent ConcurrencyPolicy = "Replace"
  )
  ```
  - design our status which contains any information we want users or other controllers to be
    able to easily obtain.
  ```
      // CronJobStatus defines the observed state of CronJob
  type CronJobStatus struct {
      // INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
      // Important: Run "make" to regenerate code after modifying this file

      // A list of pointers to currently running jobs.
      // +optional
      Active []corev1.ObjectReference `json:"active,omitempty"`

      // Information when was the last time the job was successfully scheduled.
      // +optional
      LastScheduleTime *metav1.Time `json:"lastScheduleTime,omitempty"`
  }
  ```
  - now that we have the api we can create a controller to implement this functionality
   - envisioned logic
   ```
    -Load the named CronJob
    -List all active jobs, and update the status
    -Clean up old jobs according to the history limits
    -Check if we’re suspended (and don’t do anything else if we are)
    -Get the next scheduled run
    -Run a new job if it’s on schedule, not past the deadline, and not blocked by our concurrency policy
    -Requeue when we either see a running job (done automatically) or it’s time for the next scheduled run.
   ```

   - Load the cronjob by name
     to fetch a Cronjob we will need to use our client
      - All client methods take a context (to allow for cancellation) as their first argument, and the object in
      question as their last.
   - List all active jobs and update the status
      - to do this we need to list all child jobs in this namespace that belong to this Cronjob
      - We can use List to get all the child objects

   - clean up old jobs according to the history limit
   - check if we are suspended
   - get the next scheduled run
 
- implementing the defaulting /validating webhooks 
 - kubebuilder takes care of the following for you 
    - creating a webhook server 
    - ensuring the server has been added in the manager 
    - creating handlers for your webhooks 
    - registering each handler with a path in your server 
 
 ###### setting up a webhook with kubebuilder

 1. to scaffold a webhook 
    ```
       kubebuilder create webhook --group batch --version v1 --kind CronJob --defaulting --programmatic-validation
    ```
 2. Setting up the logger for our webhook 

   ```
   var cronjoblog = logf.Log.WithName("cronjob-resource")
   ```
 3. Setting up the webhook manager 
    ```
    func (r *CronJob) SetupWebhookWithManager(mgr ctrl.Manager) error {
    return ctrl.NewWebhookManagedBy(mgr).
        For(r).
        Complete()
    }
    ```
 4. Update the ValdateCreate ValidateUpdate ValidateDelete methods 

 5. Validate the name and the Spec of the CronJob


 ###### deploying a cert manager 
  - we will use cert manager to provide certificates for the webhook server 
    - documentation to install cert manager 
    ```
     kubectl apply -f https://github.com/jetstack/cert-manager/releases/download/v1.1.0/cert-manager.yaml  
    ```
 - confirm installation 
   ```
   kubectl get pods --namespace cert-manager

   ``` 

ran into an isssue where 

1. Build your image 
2. 
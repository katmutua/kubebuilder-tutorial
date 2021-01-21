/*


Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controllers

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/go-logr/logr"
	"github.com/robfig/cron"

	kbatch "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s.io/apimachinery/pkg/runtime"
	ref "k8s.io/client-go/tools/reference"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	batchv1 "kubebuilder-tutorial/api/v1"
)

// CronJobReconciler reconciles a CronJob object
type CronJobReconciler struct {
	//added by default these allow to log, and needs to be able to fetch objects,
	client.Client
	Log    logr.Logger
	Scheme *runtime.Scheme
	Clock
}

// Clock
type realClock struct{}

func (_ realClock) Now() time.Time { return time.Now() }

// clock knows how to get the current time.
// It can be used to fake out timing for testing.
type Clock interface {
	Now() time.Time
}

// since controllers eventually run in a cluster they
// need rbac permissions to run therefore
// these bare minimum permissions are added by specifying the rbac markers below

// +kubebuilder:rbac:groups=batch.tutorial.kubebuilder.io,resources=cronjobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=batch.tutorial.kubebuilder.io,resources=cronjobs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=batch,resources=jobs/status,verbs=get
var (
    scheduledTimeAnnotation = "batch.tutorial.kubebuilder.io/scheduled-at"
)

func (r *CronJobReconciler) Reconcile(req ctrl.Request) (ctrl.Result, error) {
	ctx := context.Background()
  log := r.Log.WithValues("cronjob", req.NamespacedName)

	var cronJob batch.CronJob
    if err := r.Get(ctx, req.NamespacedName, &cronJob); err != nil {
        log.Error(err, "unable to fetch CronJob")
        // we'll ignore not-found errors, since they can't be fixed by an immediate
        // requeue (we'll need to wait for a new notification), and we can get them
        // on deleted requests.
        return ctrl.Result{}, client.IgnoreNotFound(err)
    }

// list all the child jobs
	var childJobs kbatch.JobList
	    if err := r.List(ctx, &childJobs, client.InNamespace(req.Namespace), client.MatchingFields{jobOwnerKey: req.Name}); err != nil {
	        log.Error(err, "unable to list child Jobs")
	        return ctrl.Result{}, err
	    }
// find the active list of jobs
  var activeJobs []*kbatch.Job
  var successfulJobs []*kbatch.Job
  var failedJobs []*kbatch.Job
  var mostRecentTime *time.Time // find the last run so we can update the status
	// isJobFinished
	// getScheduledTimeForJob
  for i, job := range childJobs.Items {
      _, finishedType := isJobFinished(&job)
      switch finishedType {
      case "": // ongoing
          activeJobs = append(activeJobs, &childJobs.Items[i])
      case kbatch.JobFailed:
          failedJobs = append(failedJobs, &childJobs.Items[i])
      case kbatch.JobComplete:
          successfulJobs = append(successfulJobs, &childJobs.Items[i])
      }

      // We'll store the launch time in an annotation, so we'll reconstitute that from
      // the active jobs themselves.
      scheduledTimeForJob, err := getScheduledTimeForJob(&job)
      if err != nil {
          log.Error(err, "unable to parse schedule time for child job", "job", &job)
          continue
      }
      if scheduledTimeForJob != nil {
          if mostRecentTime == nil {
              mostRecentTime = scheduledTimeForJob
          } else if mostRecentTime.Before(*scheduledTimeForJob) {
              mostRecentTime = scheduledTimeForJob
          }
      }
  }

  if mostRecentTime != nil {
      cronJob.Status.LastScheduleTime = &metav1.Time{Time: *mostRecentTime}
  } else {
      cronJob.Status.LastScheduleTime = nil
  }
  cronJob.Status.Active = nil
  for _, activeJob := range activeJobs {
      jobRef, err := ref.GetReference(r.Scheme, activeJob)
      if err != nil {
          log.Error(err, "unable to make reference to active job", "job", activeJob)
          continue
      }
      cronJob.Status.Active = append(cronJob.Status.Active, *jobRef)
  }

	log.V(1).Info("job count", "active jobs", len(activeJobs), "successful jobs", len(successfulJobs), "failed jobs", len(failedJobs))
	if err := r.Status().Update(ctx, &cronJob); err != nil {
        log.Error(err, "unable to update CronJob status")
        return ctrl.Result{}, err
    }
		// NB: deleting these is "best effort" -- if we fail on a particular one,
		    // we won't requeue just to finish the deleting.
  if cronJob.Spec.FailedJobsHistoryLimit != nil {
      sort.Slice(failedJobs, func(i, j int) bool {
          if failedJobs[i].Status.StartTime == nil {
              return failedJobs[j].Status.StartTime != nil
          }
          return failedJobs[i].Status.StartTime.Before(failedJobs[j].Status.StartTime)
      })
      for i, job := range failedJobs {
          if int32(i) >= int32(len(failedJobs))-*cronJob.Spec.FailedJobsHistoryLimit {
              break
          }
          if err := r.Delete(ctx, job, client.PropagationPolicy(metav1.DeletePropagationBackground)); client.IgnoreNotFound(err) != nil {
              log.Error(err, "unable to delete old failed job", "job", job)
          } else {
              log.V(0).Info("deleted old failed job", "job", job)
          }
      }
  }

  if cronJob.Spec.SuccessfulJobsHistoryLimit != nil {
      sort.Slice(successfulJobs, func(i, j int) bool {
          if successfulJobs[i].Status.StartTime == nil {
              return successfulJobs[j].Status.StartTime != nil
          }
          return successfulJobs[i].Status.StartTime.Before(successfulJobs[j].Status.StartTime)
      })
      for i, job := range successfulJobs {
          if int32(i) >= int32(len(successfulJobs))-*cronJob.Spec.SuccessfulJobsHistoryLimit {
              break
          }
          if err := r.Delete(ctx, job, client.PropagationPolicy(metav1.DeletePropagationBackground)); (err) != nil {
              log.Error(err, "unable to delete old successful job", "job", job)
          } else {
              log.V(0).Info("deleted old successful job", "job", job)
          }
      }
  }
	if cronJob.Spec.Suspend != nil && *cronJob.Spec.Suspend {
    log.V(1).Info("cronjob suspended, skipping")
    return ctrl.Result{}, nil
	}

	getNextSchedule := func(cronJob *batch.CronJob, now time.Time) (lastMissed time.Time, next time.Time, err error) {
        sched, err := cron.ParseStandard(cronJob.Spec.Schedule)
        if err != nil {
            return time.Time{}, time.Time{}, fmt.Errorf("Unparseable schedule %q: %v", cronJob.Spec.Schedule, err)
        }

        // for optimization purposes, cheat a bit and start from our last observed run time
        // we could reconstitute this here, but there's not much point, since we've
        // just updated it.
        var earliestTime time.Time
        if cronJob.Status.LastScheduleTime != nil {
            earliestTime = cronJob.Status.LastScheduleTime.Time
        } else {
            earliestTime = cronJob.ObjectMeta.CreationTimestamp.Time
        }
        if cronJob.Spec.StartingDeadlineSeconds != nil {
            // controller is not going to schedule anything below this point
            schedulingDeadline := now.Add(-time.Second * time.Duration(*cronJob.Spec.StartingDeadlineSeconds))

            if schedulingDeadline.After(earliestTime) {
                earliestTime = schedulingDeadline
            }
        }
        if earliestTime.After(now) {
            return time.Time{}, sched.Next(now), nil
        }

        starts := 0
        for t := sched.Next(earliestTime); !t.After(now); t = sched.Next(t) {
            lastMissed = t
            // An object might miss several starts. For example, if
            // controller gets wedged on Friday at 5:01pm when everyone has
            // gone home, and someone comes in on Tuesday AM and discovers
            // the problem and restarts the controller, then all the hourly
            // jobs, more than 80 of them for one hourly scheduledJob, should
            // all start running with no further intervention (if the scheduledJob
            // allows concurrency and late starts).
            //
            // However, if there is a bug somewhere, or incorrect clock
            // on controller's server or apiservers (for setting creationTimestamp)
            // then there could be so many missed start times (it could be off
            // by decades or more), that it would eat up all the CPU and memory
            // of this controller. In that case, we want to not try to list
            // all the missed start times.
            starts++
            if starts > 100 {
                // We can't get the most recent times so just return an empty slice
                return time.Time{}, time.Time{}, fmt.Errorf("Too many missed start times (> 100). Set or decrease .spec.startingDeadlineSeconds or check clock skew.")
            }
        }
        return lastMissed, sched.Next(now), nil
    }
    // figure out the next times that we need to create
    // jobs at (or anything we missed).
    missedRun, nextRun, err := getNextSchedule(&cronJob, r.Now())
    if err != nil {
        log.Error(err, "unable to figure out CronJob schedule")
        // we don't really care about requeuing until we get an update that
        // fixes the schedule, so don't return an error
        return ctrl.Result{}, nil
    }

		scheduledResult := ctrl.Result{RequeueAfter: nextRun.Sub(r.Now())} // save this so we can re-use it elsewhere
    log = log.WithValues("now", r.Now(), "next run", nextRun)

		if missedRun.IsZero() {
		        log.V(1).Info("no upcoming scheduled times, sleeping until next")
		        return scheduledResult, nil
		    }

		    // make sure we're not too late to start the run
		    log = log.WithValues("current run", missedRun)
		    tooLate := false
		    if cronJob.Spec.StartingDeadlineSeconds != nil {
		        tooLate = missedRun.Add(time.Duration(*cronJob.Spec.StartingDeadlineSeconds) * time.Second).Before(r.Now())
		    }
		    if tooLate {
		        log.V(1).Info("missed starting deadline for last run, sleeping till next")
		        // TODO(directxman12): events
		        return scheduledResult, nil
		    }
				// figure out how to run this job -- concurrency policy might forbid us from running
				    // multiple at the same time...
				    if cronJob.Spec.ConcurrencyPolicy == batch.ForbidConcurrent && len(activeJobs) > 0 {
				        log.V(1).Info("concurrency policy blocks concurrent runs, skipping", "num active", len(activeJobs))
				        return scheduledResult, nil
				    }

				    // ...or instruct us to replace existing ones...
				    if cronJob.Spec.ConcurrencyPolicy == batch.ReplaceConcurrent {
				        for _, activeJob := range activeJobs {
				            // we don't care if the job was already deleted
				            if err := r.Delete(ctx, activeJob, client.PropagationPolicy(metav1.DeletePropagationBackground)); client.IgnoreNotFound(err) != nil {
				                log.Error(err, "unable to delete active job", "job", activeJob)
				                return ctrl.Result{}, err
				            }
				        }
				    }
						constructJobForCronJob := func(cronJob *batch.CronJob, scheduledTime time.Time) (*kbatch.Job, error) {
        // We want job names for a given nominal start time to have a deterministic name to avoid the same job being created twice
        name := fmt.Sprintf("%s-%d", cronJob.Name, scheduledTime.Unix())

        job := &kbatch.Job{
            ObjectMeta: metav1.ObjectMeta{
                Labels:      make(map[string]string),
                Annotations: make(map[string]string),
                Name:        name,
                Namespace:   cronJob.Namespace,
            },
            Spec: *cronJob.Spec.JobTemplate.Spec.DeepCopy(),
        }
        for k, v := range cronJob.Spec.JobTemplate.Annotations {
            job.Annotations[k] = v
        }
        job.Annotations[scheduledTimeAnnotation] = scheduledTime.Format(time.RFC3339)
        for k, v := range cronJob.Spec.JobTemplate.Labels {
            job.Labels[k] = v
        }
        if err := ctrl.SetControllerReference(cronJob, job, r.Scheme); err != nil {
            return nil, err
        }

        return job, nil
    }

		/ actually make the job...
    job, err := constructJobForCronJob(&cronJob, missedRun)
    if err != nil {
        log.Error(err, "unable to construct job from template")
        // don't bother requeuing until we get a change to the spec
        return scheduledResult, nil
    }

    // ...and create it on the cluster
    if err := r.Create(ctx, job); err != nil {
        log.Error(err, "unable to create Job for CronJob", "job", job)
        return ctrl.Result{}, err
    }

    log.V(1).Info("created Job for CronJob run", "job", job)
		// we'll requeue once we see the running job, and update our status
		    return scheduledResult, nil
}

var (
	jobOwnerKey = ".metadata.controller"
	apiGVStr    = batch.GroupVersion.String()
)

func (r *CronJobReconciler) SetupWithManager(mgr ctrl.Manager) error {
    // set up a real clock, since we're not in a test
    if r.Clock == nil {
        r.Clock = realClock{}
    }

    if err := mgr.GetFieldIndexer().IndexField(&kbatch.Job{}, jobOwnerKey, func(rawObj runtime.Object) []string {
        // grab the job object, extract the owner...
        job := rawObj.(*kbatch.Job)
        owner := metav1.GetControllerOf(job)
        if owner == nil {
            return nil
        }
        // ...make sure it's a CronJob...
        if owner.APIVersion != apiGVStr || owner.Kind != "CronJob" {
            return nil
        }

        // ...and if so, return it
        return []string{owner.Name}
    }); err != nil {
        return err
    }

    return ctrl.NewControllerManagedBy(mgr).
        For(&batch.CronJob{}).
        Owns(&kbatch.Job{}).
        Complete(r)
}

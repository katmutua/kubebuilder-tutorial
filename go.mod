module kubebuilder-tutorial

go 1.13

require (
	github.com/go-logr/logr v0.3.0
	github.com/onsi/ginkgo v1.14.2
	github.com/onsi/gomega v1.10.4
	github.com/robfig/cron v1.2.0
	k8s.io/api v0.19.0
    k8s.io/apimachinery v0.19.0
    k8s.io/client-go v0.19.0
	k8s.io/client-go v11.0.0+incompatible
	sigs.k8s.io/controller-runtime v0.5.0
)
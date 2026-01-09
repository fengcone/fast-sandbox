package main

import (
	"context"
	"flag"
	"os"

	"fast-sandbox/internal/api"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	apiv1alpha1 "fast-sandbox/api/v1alpha1"
	"fast-sandbox/internal/controller"
	"fast-sandbox/internal/controller/agentcontrol"
	"fast-sandbox/internal/controller/agentpool"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(apiv1alpha1.AddToScheme(scheme))
}

func main() {
	var metricsAddr string
	var probeAddr string
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")

	opts := zap.Options{Development: true}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress: metricsAddr,
		},
		HealthProbeBindAddress: probeAddr,
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	reg := agentpool.NewInMemoryRegistry()
	agentHTTPClient := api.NewAgentClient()
	if err = (&controller.SandboxReconciler{
		Client:      mgr.GetClient(),
		Scheme:      mgr.GetScheme(),
		Ctx:         context.Background(),
		Registry:    reg,
		AgentClient: agentHTTPClient,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Sandbox")
		os.Exit(1)
	}

	if err = (&controller.SandboxPoolReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Registry: reg,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "SandboxPool")
		os.Exit(1)
	}

	// 启动 AgentControlLoop
	ctx := ctrl.SetupSignalHandler()
	loop := agentcontrol.NewLoop(mgr.GetClient(), reg, agentHTTPClient)
	go loop.Start(ctx)

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	
	// 崩溃恢复：在启动 Manager 之后，异步执行一次性状态恢复
	go func() {
		// 等待缓存同步
		if mgr.GetCache().WaitForCacheSync(context.Background()) {
			setupLog.Info("Cache synced, restoring registry state from cluster")
			if err := reg.Restore(context.Background(), mgr.GetClient()); err != nil {
				setupLog.Error(err, "failed to restore registry state")
			} else {
				setupLog.Info("Registry state restored successfully")
			}
		}
	}()

	if err := mgr.Start(ctx); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

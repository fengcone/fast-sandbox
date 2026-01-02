package main

import (
	"context"
	"flag"
	"os"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	apiv1alpha1 "fast-sandbox/api/v1alpha1"
	"fast-sandbox/internal/controller"
	"fast-sandbox/internal/controller/agentclient"
	"fast-sandbox/internal/controller/agentcontrol"
	"fast-sandbox/internal/controller/agentpool"
	"fast-sandbox/internal/controller/agentserver"
	"fast-sandbox/internal/controller/scheduler"
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
	opts := zap.Options{Development: true}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	reg := agentpool.NewInMemoryRegistry()
	sched := scheduler.NewSimpleScheduler()
	agentHTTPClient := agentclient.NewAgentClient()

	// 启动 HTTP Server 接收 Agent 注册和心跳
	agentHTTPServer := agentserver.NewServer(reg, ":9090")
	go func() {
		if err := agentHTTPServer.Start(); err != nil {
			setupLog.Error(err, "agent HTTP server failed")
		}
	}()

	if err = (&controller.SandboxClaimReconciler{
		Client:    mgr.GetClient(),
		Scheme:    mgr.GetScheme(),
		Ctx:       context.Background(),
		Registry:  reg,
		Scheduler: sched,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "SandboxClaim")
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
	if err := mgr.Start(ctx); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

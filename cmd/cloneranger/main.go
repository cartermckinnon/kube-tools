package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path"
	"runtime"
	"strings"

	"github.com/cartermckinnon/kube-tools/internal/workerpool"
	"github.com/integrii/flaggy"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	cloneLabelKey                = "cloneranger-clone"
	cloneTemplateLabel           = "cloneranger-template"
	nodeRoleControlPlaneLabelKey = "node-role.kubernetes.io/control-plane"
)

type options struct {
	action                    string
	kubeconfig                string
	templateNodeNames         []string
	templateNodeLabelSelector string
	perTemplate               int
	concurrency               int
	dryRun                    bool
}

func main() {
	flaggy.SetName("cloneranger")
	flaggy.SetDescription("Manage cloned nodes in a Kubernetes cluster")

	homeDir, err := os.UserHomeDir()
	if err != nil {
		log.Fatalf("failed to get user home directory: %v", err)
	}

	opts := options{
		kubeconfig:  path.Join(homeDir, ".kube", "config"),
		perTemplate: 10,
		concurrency: runtime.NumCPU(),
		dryRun:      false,
	}

	flaggy.AddPositionalValue(&opts.action, "action", 1, true, "Action to perform: up or down")

	flaggy.String(&opts.kubeconfig, "", "kubeconfig", "path to kubeconfig (defaults to $HOME/.kube/config)")
	flaggy.StringSlice(&opts.templateNodeNames, "", "templates", "comma-separated list of template node names (default: all non-clone nodes)")
	flaggy.String(&opts.templateNodeLabelSelector, "", "template-label-selector", "label selector to filter template nodes")
	flaggy.Int(&opts.perTemplate, "n", "per-template", "Number of clones to create per template node")
	flaggy.Int(&opts.concurrency, "", "concurrency", "Concurrent API operations")
	flaggy.Bool(&opts.dryRun, "", "dry-run", "Skip any mutating API operations")

	flaggy.Parse()

	if opts.dryRun {
		log.SetPrefix("[DRY-RUN] ")
	}

	client, err := buildClient(opts.kubeconfig)
	if err != nil {
		log.Fatalf("failed to build kube client: %v\n", err)
	}

	switch opts.action {
	case "up":
		if err := cmdUp(client, opts); err != nil {
			log.Fatalf("up failed: %v", err)
		}
	case "down":
		if err := cmdDown(client, opts); err != nil {
			log.Fatalf("down failed: %v", err)
		}
	default:
		log.Fatalf("unknown action: %q", opts.action)
	}
}

func buildClient(kubeconfigPath string) (*kubernetes.Clientset, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		if kubeconfigPath == "" {
			return nil, errors.New("no kubeconfig provided and not running in-cluster")
		}
		cfg, err = clientcmd.BuildConfigFromFlags("", kubeconfigPath)
		if err != nil {
			return nil, err
		}
	}
	// disable client-side rate limiting, we want to go as fast as APF allows
	cfg.QPS = -1
	cfg.Burst = -1
	return kubernetes.NewForConfig(cfg)
}

func resolveTemplates(client *kubernetes.Clientset, opts options) ([]v1.Node, error) {
	if len(opts.templateNodeNames) == 0 {
		labelSelectors := []string{
			"!" + cloneLabelKey,
			"!" + nodeRoleControlPlaneLabelKey, // always exclude control-plane nodes
		}
		if opts.templateNodeLabelSelector != "" {
			labelSelectors = append(labelSelectors, opts.templateNodeLabelSelector)
		}
		nl, err := client.CoreV1().Nodes().List(context.Background(), metav1.ListOptions{
			LabelSelector: strings.Join(labelSelectors, ","),
		})
		if err != nil {
			return nil, err
		}
		return nl.Items, nil
	}
	var out []v1.Node
	for _, name := range opts.templateNodeNames {
		n, err := client.CoreV1().Nodes().Get(context.Background(), name, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("failed to get template node %q: %w", name, err)
		}
		out = append(out, *n)
	}
	return out, nil
}

func createClone(ctx context.Context, client *kubernetes.Clientset, templateNode v1.Node, dryRun bool) error {
	node := templateNode.DeepCopy()

	node.Name = templateNode.Name + "-clone"
	node.ResourceVersion = ""
	node.Labels[cloneLabelKey] = "true"
	node.Labels[cloneTemplateLabel] = templateNode.Name

	if !dryRun {
		node.GenerateName = node.Name + "-"
		node.Name = ""
		if newNode, err := client.CoreV1().Nodes().Create(ctx, node, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("failed to create node %s: %w", node.GenerateName, err)
		} else {
			node.Name = newNode.Name // save generated name
		}
	}
	log.Printf("created node %s", node.Name)

	return nil
}

func cmdUp(client *kubernetes.Clientset, opts options) error {
	templateNodes, err := resolveTemplates(client, opts)
	if err != nil {
		return fmt.Errorf("failed to resolve template nodes: %w", err)
	}
	if len(templateNodes) == 0 {
		return errors.New("no template nodes found")
	}

	workerPool := workerpool.New(opts.concurrency).Start()

	for _, templateNode := range templateNodes {
		for i := 1; i <= opts.perTemplate; i++ {
			workerPool.Submit(func() error {
				if err := createClone(context.Background(), client, templateNode, opts.dryRun); err != nil {
					return fmt.Errorf("failed to create clone: %w", err)
				}
				return nil
			})
		}
	}

	return errors.Join(workerPool.Stop()...)
}

func cmdDown(client *kubernetes.Clientset, opts options) error {
	labelSelectors := []string{cloneLabelKey + "=true"}
	if opts.templateNodeLabelSelector != "" {
		labelSelectors = append(labelSelectors, opts.templateNodeLabelSelector)
	}
	if len(opts.templateNodeNames) > 0 {
		labelSelectors = append(labelSelectors, fmt.Sprintf("%s in (%s)", cloneTemplateLabel, strings.Join(opts.templateNodeNames, ",")))
	}
	nl, err := client.CoreV1().Nodes().List(context.Background(), metav1.ListOptions{
		LabelSelector: strings.Join(labelSelectors, ","),
	})
	if err != nil {
		return fmt.Errorf("failed to list clone nodes: %w", err)
	}

	workerPool := workerpool.New(opts.concurrency).Start()

	for _, node := range nl.Items {
		workerPool.Submit(func() error {
			if !opts.dryRun {
				if err := client.CoreV1().Nodes().Delete(context.Background(), node.Name, metav1.DeleteOptions{}); err != nil {
					return fmt.Errorf("failed to delete node %s: %w", node.Name, err)
				}
			}
			log.Printf("deleted node: %s", node.Name)
			return nil
		})
	}

	return errors.Join(workerPool.Stop()...)
}

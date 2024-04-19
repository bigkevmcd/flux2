/*
Copyright 2024 The Flux authors

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

package main

import (
	"context"
	"fmt"
	"slices"

	"github.com/emicklei/dot"
	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/fluxcd/cli-utils/pkg/object"
	"github.com/fluxcd/flux2/v2/internal/tree"
	"github.com/fluxcd/flux2/v2/internal/utils"
	kustomizev1 "github.com/fluxcd/kustomize-controller/api/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

var depsKsCmd = &cobra.Command{
	Use:     "kustomization [name]",
	Aliases: []string{"ks", "kustomization"},
	Short:   "Print the state of dependencies for Kustomizations",
	Long:    withPreviewNote(`The tree command prints the state of the Kustomization.'`),
	Example: `  # Print the tree of dependencies of the named kustomization
  flux deps kustomization flux-system`,
	RunE:              depsKsCmdRun,
	ValidArgsFunction: resourceNamesCompletionFunc(kustomizev1.GroupVersion.WithKind(kustomizev1.KustomizationKind)),
}

type DepsKsFlags struct {
	output string
}

var depsKsArgs DepsKsFlags

func init() {
	depsKsCmd.Flags().StringVarP(&treeKsArgs.output, "output", "o", "",
		"the format in which the dependencies should be printed. can be 'dot'")
	depsCmd.AddCommand(depsKsCmd)
}

func depsKsCmdRun(cmd *cobra.Command, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("kustomization name is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), rootArgs.timeout)
	defer cancel()

	kubeClient, err := utils.KubeClient(kubeconfigArgs, kubeclientOptions)
	if err != nil {
		return err
	}

	name := args[0]
	k := &kustomizev1.Kustomization{}
	err = kubeClient.Get(ctx, client.ObjectKey{
		Namespace: *kubeconfigArgs.Namespace,
		Name:      name,
	}, k)
	if err != nil {
		return err
	}

	switch treeKsArgs.output {
	case "dot":
		g, err := buildDotGraph(ctx, kubeClient, k)
		if err != nil {
			return err
		}
		rootCmd.Println(g.String())
	default:
		// g := buildTree(ctx, kubeClient, k)
		// rootCmd.Println(g.Print())
	}

	return nil
}

func buildTree(ctx context.Context, kubeClient client.Client, kustomizations []kustomizev1.Kustomization) tree.ObjMetadataTree {
	kTree := tree.New(object.ObjMetadata{
		GroupKind: schema.GroupKind{
			Group: kustomizev1.GroupVersion.Group,
			Kind:  kustomizev1.KustomizationKind,
		},
	})

	for _, k := range kustomizations {
		subTree := buildKustomizationTree(ctx, kubeClient, k)
		kTree.AddTree(subTree)
	}

	return kTree

}

func buildKustomizationTree(ctx context.Context, kubeClient client.Client, kustomization kustomizev1.Kustomization) tree.ObjMetadataTree {
	kTree := tree.New(object.ObjMetadata{
		Name:      kustomization.GetName(),
		Namespace: kustomization.GetNamespace(),
		GroupKind: schema.GroupKind{
			Group: kustomizev1.GroupVersion.Group,
			Kind:  kustomizev1.KustomizationKind,
		},
	})

	return kTree
}

func buildDotGraph(ctx context.Context, kubeClient client.Client, kustomization *kustomizev1.Kustomization) (*dot.Graph, error) {
	g := dot.NewGraph(dot.Directed)
	idsToNodes := buildKustomizationNodes(ctx, kustomization, g)
	if err := addKustomizationDependsOn(ctx, kubeClient, kustomization, g, idsToNodes); err != nil {
		return nil, err
	}

	return g, nil
}

func buildKustomizationNodes(ctx context.Context, kustomization *kustomizev1.Kustomization, g *dot.Graph) map[string]dot.Node {
	idsToNodes := map[string]dot.Node{}

	key := kustomizationObjectKey(kustomization)
	colour := "red"
	if isReady(kustomization.Status.Conditions) {
		colour = "green"
	}
	node := g.Node(key).Attr("color", colour)
	idsToNodes[key] = node

	return idsToNodes
}

func addKustomizationDependsOn(ctx context.Context, kubeClient client.Client, kustomization *kustomizev1.Kustomization, g *dot.Graph, idsToNodes map[string]dot.Node) error {
	for _, dep := range kustomization.Spec.DependsOn {
		parentNode := idsToNodes[kustomizationObjectKey(kustomization)]

		dependsOn := client.ObjectKey{
			Name:      dep.Name,
			Namespace: dep.Namespace,
		}

		if dependsOn.Namespace == "" {
			dependsOn.Namespace = kustomization.GetNamespace()
		}

		colour := "red"
		ks, err := getKustomization(ctx, kubeClient, dependsOn)
		if err != nil {
			return fmt.Errorf("loading dependency for kustomization: %s: %w", kustomizationObjectKey(kustomization), err)
		}
		if ks != nil {
			kustomization = ks
		}

		if kustomization != nil {
			if isReady(kustomization.Status.Conditions) {
				colour = "green"
			}
		} else {
			colour = "amber"
		}

		depNode := g.Node(dependsOn.String()).Attr("color", colour)
		g.Edge(parentNode, depNode)
	}

	return nil
}

func getKustomization(ctx context.Context, kubeClient client.Client, name client.ObjectKey) (*kustomizev1.Kustomization, error) {
	var kustomization kustomizev1.Kustomization
	err := kubeClient.Get(ctx, name, &kustomization)
	if err == nil {
		return &kustomization, nil
	}

	if apierrors.IsForbidden(err) {
		return nil, nil
	}

	return nil, err
}

func kustomizationObjectKey(k *kustomizev1.Kustomization) string {
	return client.ObjectKeyFromObject(k).String()
}

func isReady(conds []metav1.Condition) bool {
	return slices.ContainsFunc(conds, func(cond metav1.Condition) bool {
		return cond.Status == metav1.ConditionTrue && cond.Type == "Ready"
	})
}

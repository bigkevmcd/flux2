//go:build unit
// +build unit

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
	"errors"
	"strings"
	"testing"

	kustomizev1 "github.com/fluxcd/kustomize-controller/api/v1"
	"github.com/fluxcd/pkg/apis/meta"
	"github.com/google/go-cmp/cmp"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestBuildDotGraph(t *testing.T) {
	kustomization1 := newTestKustomization("kustomization-1")
	kustomization2 := newTestKustomization("kustomization-2")
	kustomization3 := newTestKustomization("kustomization-3", withDependsOn("kustomization-1"), withReadyStatus)
	kustomization4 := newTestKustomization("kustomization-4", withDependsOn("kustomization-2", "kustomization-3"))
	kustomization5 := newTestKustomization("kustomization-5", withDependsOn("kustomization-4"))

	fc := newFakeClient(t, kustomization1, kustomization2, kustomization3, kustomization4, kustomization5)

	d, err := buildDotGraph(context.Background(), fc, kustomization1)
	if err != nil {
		t.Fatal(err)
	}

	want := `digraph  {
	
	n1[color="red",label="default/kustomization-1"];
	n2[color="red",label="default/kustomization-2"];
	n3[color="green",label="default/kustomization-3"];
	n4[color="red",label="default/kustomization-4"];
	n3->n1;
	n4->n2;
	n4->n3;
	
}
`

	if diff := cmp.Diff(want, d.String()); diff != "" {
		t.Fatalf("failed to visualise dependencies: %s\n", diff)
	}
}

func TestBuildDotGraph_dependency_in_other_namespace(t *testing.T) {
	kustomization1 := newTestKustomization("kustomization-1", withDependsOn("testing/kustomization-2"), withReadyStatus)
	kustomization2 := newTestKustomization("kustomization-2", inNamespace("testing"), withReadyStatus)

	fc := newFakeClient(t, kustomization1, kustomization2)

	d, err := buildDotGraph(context.Background(), fc, kustomization1)
	if err != nil {
		t.Fatal(err)
	}

	want := `digraph  {
	
	n1[color="green",label="default/kustomization-1"];
	n2[color="green",label="testing/kustomization-2"];
	n1->n2;
	
}
`

	if diff := cmp.Diff(want, d.String()); diff != "" {
		t.Fatalf("failed to visualise dependencies: %s\n", diff)
	}
}

func TestBuildDotGraph_dependency_not_found(t *testing.T) {
	kustomization1 := newTestKustomization("kustomization-1", withDependsOn("testing/kustomization-2"), withReadyStatus)

	fc := newFakeClient(t, kustomization1)

	_, err := buildDotGraph(context.Background(), fc, kustomization1)
	if !apierrors.IsNotFound(err) {
		t.Errorf("got error %v, wanted NotFound", err)
	}

	want := `loading dependency for kustomization: default/kustomization-1: kustomizations.kustomize.toolkit.fluxcd.io "kustomization-2" not found`
	if msg := err.Error(); msg != want {
		t.Errorf("got error %s, want %s", msg, want)
	}
}

func TestBuildDotGraph_dependency_not_allowed(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kustomizev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	kustomization1 := newTestKustomization("kustomization-1", withDependsOn("testing/kustomization-2"), withReadyStatus)

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(kustomization1).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, kc client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				want := client.ObjectKey{Name: "kustomization-2", Namespace: "testing"}
				if key == want {
					return apierrors.NewForbidden(schema.GroupResource{}, "kustomization-2", errors.New("just a test error"))
				}
				return kc.Get(ctx, key, obj)
			},
		}).
		Build()

	d, err := buildDotGraph(context.Background(), fakeClient, kustomization1)
	if err != nil {
		t.Fatal(err)
	}

	want := `digraph  {
	
	n1[color="green",label="default/kustomization-1"];
	n2[color="amber",label="testing/kustomization-2"];
	n1->n2;
	
}
`
	if diff := cmp.Diff(want, d.String()); diff != "" {
		t.Fatalf("failed to visualise dependencies: %s\n", diff)
	}
}

func withDependsOn(s ...string) func(k *kustomizev1.Kustomization) {
	dependsOn := s
	return func(k *kustomizev1.Kustomization) {
		for _, v := range dependsOn {
			var name string = v
			var ns string
			if elements := strings.Split(v, "/"); len(elements) > 1 {
				ns = elements[0]
				name = elements[1]
			}
			k.Spec.DependsOn = append(k.Spec.DependsOn, meta.NamespacedObjectReference{
				Name:      name,
				Namespace: ns,
			})
		}
	}
}

func TestBuildKustomizationTree(t *testing.T) {
	kustomization1 := newTestKustomization("kustomization-1")
	kustomization2 := newTestKustomization("kustomization-2")
	kustomization3 := newTestKustomization("kustomization-3", withDependsOn("kustomization-1"), withReadyStatus)
	kustomization4 := newTestKustomization("kustomization-4", withDependsOn("kustomization-2", "kustomization-3"))
	kustomization5 := newTestKustomization("kustomization-5", withDependsOn("kustomization-4"))

	fc := newFakeClient(t, kustomization1, kustomization2, kustomization3, kustomization4, kustomization5)

	out := buildTree(context.TODO(), fc, kustomization1).Print()

	want := ``
	if diff := cmp.Diff(want, out); diff != "" {
		t.Fatalf("failed to render tree: %s\n", diff)
	}
}

func withReadyStatus(k *kustomizev1.Kustomization) {
	k.Status.Conditions = append(k.Status.Conditions, metav1.Condition{
		Status: metav1.ConditionTrue,
		Type:   "Ready",
	})
}

func inNamespace(ns string) func(k *kustomizev1.Kustomization) {
	return func(k *kustomizev1.Kustomization) {
		k.ObjectMeta.Namespace = ns
	}
}

func newTestKustomization(n string, opts ...func(k *kustomizev1.Kustomization)) *kustomizev1.Kustomization {
	k := kustomizev1.Kustomization{
		ObjectMeta: metav1.ObjectMeta{
			Name:      n,
			Namespace: "default",
		},
	}

	for _, o := range opts {
		o(&k)
	}

	return &k
}

func newFakeClient(t *testing.T, objs ...runtime.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := kustomizev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	return fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(objs...).
		Build()
}

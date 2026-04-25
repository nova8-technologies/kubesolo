package manifests

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/rs/zerolog/log"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"
)

const fieldManager = "kubesolo-manifests"

// applyAll applies all manifest byte slices to the cluster.
// Each byte slice may contain multiple YAML documents separated by "---".
func applyAll(adminKubeconfig string, manifests [][]byte) error {
	config, err := clientcmd.BuildConfigFromFlags("", adminKubeconfig)
	if err != nil {
		return fmt.Errorf("failed to build kubeconfig: %w", err)
	}
	config.BearerToken = ""

	dynClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("failed to create dynamic client: %w", err)
	}

	discoveryClient, err := discovery.NewDiscoveryClientForConfig(config)
	if err != nil {
		return fmt.Errorf("failed to create discovery client: %w", err)
	}

	mapper, err := buildRESTMapper(discoveryClient)
	if err != nil {
		return fmt.Errorf("failed to build REST mapper: %w", err)
	}

	var applyErrors int
	for _, data := range manifests {
		objects, err := decodeMultiDoc(data)
		if err != nil {
			log.Error().Err(err).Msg("failed to decode manifest YAML")
			applyErrors++
			continue
		}

		for _, obj := range objects {
			if err := applyObject(dynClient, mapper, obj); err != nil {
				log.Error().Err(err).
					Str("kind", obj.GetKind()).
					Str("name", obj.GetName()).
					Str("namespace", obj.GetNamespace()).
					Msg("failed to apply resource")
				applyErrors++
			}
		}
	}

	if applyErrors > 0 {
		log.Warn().Str("component", "manifests").Int("errors", applyErrors).Msg("some manifests failed to apply")
	}

	return nil
}

// applyObject applies a single unstructured object using server-side apply.
func applyObject(dynClient dynamic.Interface, mapper meta.RESTMapper, obj *unstructured.Unstructured) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	gvk := obj.GroupVersionKind()
	mapping, err := mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return fmt.Errorf("failed to find REST mapping for %s: %w", gvk.String(), err)
	}

	data, err := json.Marshal(obj)
	if err != nil {
		return fmt.Errorf("failed to marshal object: %w", err)
	}

	var resource dynamic.ResourceInterface
	if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
		ns := obj.GetNamespace()
		if ns == "" {
			ns = "default"
		}
		resource = dynClient.Resource(mapping.Resource).Namespace(ns)
	} else {
		resource = dynClient.Resource(mapping.Resource)
	}

	_, err = resource.Patch(ctx, obj.GetName(), types.ApplyPatchType, data, metav1.PatchOptions{
		FieldManager: fieldManager,
	})
	if err != nil {
		return fmt.Errorf("server-side apply failed: %w", err)
	}

	log.Info().Str("component", "manifests").
		Str("kind", obj.GetKind()).
		Str("name", obj.GetName()).
		Str("namespace", obj.GetNamespace()).
		Msg("applied resource")

	return nil
}

// decodeMultiDoc splits a multi-document YAML byte slice into individual unstructured objects.
func decodeMultiDoc(data []byte) ([]*unstructured.Unstructured, error) {
	var objects []*unstructured.Unstructured
	decoder := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)

	for {
		obj := &unstructured.Unstructured{}
		err := decoder.Decode(obj)
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("failed to decode YAML document: %w", err)
		}

		// Skip empty documents
		if obj.Object == nil || len(obj.Object) == 0 {
			continue
		}

		objects = append(objects, obj)
	}

	return objects, nil
}

// buildRESTMapper creates a REST mapper from API server discovery.
func buildRESTMapper(discoveryClient discovery.DiscoveryInterface) (meta.RESTMapper, error) {
	groupResources, err := restmapper.GetAPIGroupResources(discoveryClient)
	if err != nil {
		return nil, fmt.Errorf("failed to get API group resources: %w", err)
	}
	return restmapper.NewDiscoveryRESTMapper(groupResources), nil
}

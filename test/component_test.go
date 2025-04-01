package test

import (
	"context"
	"testing"
	"fmt"
	"strings"
	"time"
	"github.com/cloudposse/test-helpers/pkg/atmos"
	"github.com/cloudposse/test-helpers/pkg/helm"
	awsHelper "github.com/cloudposse/test-helpers/pkg/aws"
	helper "github.com/cloudposse/test-helpers/pkg/atmos/component-helper"
	awsTerratest "github.com/gruntwork-io/terratest/modules/aws"
	"github.com/gruntwork-io/terratest/modules/random"

	"github.com/stretchr/testify/assert"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/tools/cache"
)

type ComponentSuite struct {
	helper.TestSuite
}

func (s *ComponentSuite) TestBasic() {
	const component = "eks/external-secrets-operator/basic"
	const stack = "default-test"
	const awsRegion = "us-east-2"

	clusterOptions := s.GetAtmosOptions("eks/cluster", stack, nil)
	clusrerId := atmos.Output(s.T(), clusterOptions, "eks_cluster_id")
	cluster := awsHelper.GetEksCluster(s.T(), context.Background(), awsRegion, clusrerId)
	clientset, err := awsHelper.NewK8SClientset(cluster)
	assert.NoError(s.T(), err)
	assert.NotNil(s.T(), clientset)

	randomID := strings.ToLower(random.UniqueId())

	namespace := fmt.Sprintf("external-secrets-%s", randomID)
	secretPathPrefix := fmt.Sprintf("test-%s", randomID)
	secretPath := fmt.Sprintf("/%s/database-credentials", secretPathPrefix)

	defer func() {
		awsTerratest.DeleteParameter(s.T(), awsRegion, secretPath)
	}()
	awsTerratest.PutParameter(s.T(), awsRegion, secretPath, "Test value", randomID)

	inputs := map[string]interface{}{
		"kubernetes_namespace": namespace,
		"parameter_store_paths": []string{secretPathPrefix},
	}

	defer s.DestroyAtmosComponent(s.T(), component, stack, &inputs)
	options, _ := s.DeployAtmosComponent(s.T(), component, stack, &inputs)
	assert.NotNil(s.T(), options)

	metadata := helm.Metadata{}

	atmos.OutputStruct(s.T(), options, "metadata", &metadata)

	assert.Equal(s.T(), metadata.AppVersion, "v0.8.3")
	assert.Equal(s.T(), metadata.Chart, "external-secrets")
	assert.NotNil(s.T(), metadata.FirstDeployed)
	assert.NotNil(s.T(), metadata.LastDeployed)
	assert.Equal(s.T(), metadata.Name, "external-secrets")
	assert.Equal(s.T(), metadata.Namespace, namespace)
	assert.NotNil(s.T(), metadata.Values)
	assert.Equal(s.T(), metadata.Version, "0.8.3")

	// Deploy second time to create ClusterSecretStore resource
	options, _ = s.DeployAtmosComponent(s.T(), component, stack, &inputs)
	assert.NotNil(s.T(), options)

	config, err := awsHelper.NewK8SClientConfig(cluster)
	assert.NoError(s.T(), err)
	assert.NotNil(s.T(), config)

	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		panic(fmt.Errorf("failed to create dynamic client: %v", err))
	}

	// Define the GroupVersionResource for the ExternalSecret CRD
	externalSecretGVR := schema.GroupVersionResource{
		Group:    "external-secrets.io",
		Version:  "v1beta1",
		Resource: "externalsecrets",
	}

	externalSecretName := fmt.Sprintf("external-secret-%s", randomID)

	// Create the ExternalSecret object as an unstructured resource
	externalSecret := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "external-secrets.io/v1beta1",
			"kind":       "ExternalSecret",
			"metadata": map[string]interface{}{
				"name":	externalSecretName,
				"namespace": namespace,
			},
			"spec": map[string]interface{}{
				"secretStoreRef": map[string]interface{}{
					"name": "secret-store-parameter-store",
					"kind": "ClusterSecretStore",
				},
				"refreshInterval": "1h",
				"target": map[string]interface{}{
					"name": externalSecretName,
				},
				"data": []interface{}{
					map[string]interface{}{
						"secretKey": "username",
						"remoteRef": map[string]interface{}{
							"key": secretPath,
						},
					},
				},
			},
		},
	}

	factory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(dynamicClient, time.Minute, corev1.NamespaceAll, nil)
	informer := factory.ForResource(externalSecretGVR).Informer()

	stopChannel := make(chan struct{})

	informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		UpdateFunc: func(oldObj, newObj interface{}) {
			externalSecret := newObj.(*unstructured.Unstructured)

			if externalSecret.GetName() != externalSecretName {
				fmt.Printf("ExternalSecret name is not '%s', it is '%s'\n", externalSecretName, externalSecret.GetName())
				return
			}

			// Ensure we are checking the status of the correct object
			conditions, found, err := unstructured.NestedSlice(externalSecret.Object, "status", "conditions")

			if err != nil || !found {
				fmt.Println("Error retrieving conditions from status")
				return
			}

			// Check if the external secret is ready
			for _, condition := range conditions {
				conditionMap := condition.(map[string]interface{})
				if conditionMap["type"] == "Ready" && conditionMap["status"] == "True" {
					close(stopChannel) // Stop the informer if the external secret is ready
					return
				}
			}
		},
	})

	go informer.Run(stopChannel)

	defer func() {
		if err := dynamicClient.Resource(externalSecretGVR).Namespace(namespace).Delete(context.Background(), externalSecretName, metav1.DeleteOptions{}); err != nil {
			fmt.Printf("Error deleting external secret %s: %v\n", externalSecretName, err)
		}
	}()

	// Create the ExternalSecret resource in the "default" namespace
	_, err = dynamicClient.Resource(externalSecretGVR).Namespace(namespace).Create(context.Background(), externalSecret, metav1.CreateOptions{})
	assert.NoError(s.T(), err)

	select {
		case <-stopChannel:
			msg := "ExternalSecret is ready"
			fmt.Println(msg)
		case <-time.After(1 * time.Minute):
			defer close(stopChannel)
			msg := "ExternalSecret is not ready"
			assert.Fail(s.T(), msg)
	}

	secret, err := clientset.CoreV1().Secrets(namespace).Get(context.Background(), externalSecretName, metav1.GetOptions{})
	assert.NoError(s.T(), err)
	assert.NotNil(s.T(), secret)

	secretData, found := secret.Data["username"]
	assert.True(s.T(), found)
	assert.Equal(s.T(), string(secretData), randomID)

	s.DriftTest(component, stack, &inputs)
}

func (s *ComponentSuite) TestEnabledFlag() {
	const component = "eks/external-secrets-operator/disabled"
	const stack = "default-test"
	s.VerifyEnabledFlag(component, stack, nil)
}

func (s *ComponentSuite) SetupSuite() {
	s.TestSuite.InitConfig()
	s.TestSuite.Config.ComponentDestDir = "components/terraform/eks/external-secrets-operator"
	s.TestSuite.SetupSuite()
}

func TestRunSuite(t *testing.T) {
	suite := new(ComponentSuite)
	suite.AddDependency(t, "vpc", "default-test", nil)
	suite.AddDependency(t, "eks/cluster", "default-test", nil)
	helper.Run(t, suite)
}

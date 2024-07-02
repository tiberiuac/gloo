package deployer_test

import (
	"context"
	"fmt"
	"slices"

	envoy_config_bootstrap "github.com/envoyproxy/go-control-plane/envoy/config/bootstrap/v3"
	_ "github.com/envoyproxy/go-control-plane/envoy/extensions/upstreams/http/v3"
	wrapperspb "github.com/golang/protobuf/ptypes/wrappers"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/types"
	"github.com/solo-io/gloo/pkg/version"
	"github.com/solo-io/gloo/projects/gateway2/controller/scheme"
	"github.com/solo-io/gloo/projects/gateway2/deployer"
	v1 "github.com/solo-io/gloo/projects/gateway2/pkg/api/external/kubernetes/api/core/v1"
	gw2_v1alpha1 "github.com/solo-io/gloo/projects/gateway2/pkg/api/gateway.gloo.solo.io/v1alpha1"
	"github.com/solo-io/gloo/projects/gateway2/pkg/api/gateway.gloo.solo.io/v1alpha1/kube"
	"github.com/solo-io/gloo/projects/gateway2/wellknown"
	"github.com/solo-io/gloo/projects/gloo/pkg/bootstrap"
	glooutils "github.com/solo-io/gloo/projects/gloo/pkg/utils"
	"github.com/solo-io/gloo/projects/gloo/pkg/xds"
	"github.com/solo-io/gloo/test/gomega/matchers"
	"github.com/solo-io/solo-kit/pkg/api/v1/resources/core"
	"github.com/solo-io/solo-kit/pkg/utils/protoutils"
	knownwrappers "google.golang.org/protobuf/types/known/wrapperspb"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	api "sigs.k8s.io/gateway-api/apis/v1"
)

// testBootstrap implements resources.Resource in order to use protoutils.UnmarshalYAML
// this is hacky but it seems more stable/concise than map-casting all the way down
// to the field we need.
type testBootstrap struct {
	envoy_config_bootstrap.Bootstrap
}

func (t *testBootstrap) SetMetadata(meta *core.Metadata) {}

func (t *testBootstrap) Equal(_ any) bool {
	return false
}

func (t *testBootstrap) GetMetadata() *core.Metadata {
	return nil
}

type clientObjects []client.Object

func (objs *clientObjects) findDeployment(namespace, name string) *appsv1.Deployment {
	for _, obj := range *objs {
		if dep, ok := obj.(*appsv1.Deployment); ok {
			if dep.Name == name && dep.Namespace == namespace {
				return dep
			}
		}
	}
	return nil
}

func (objs *clientObjects) findServiceAccount(namespace, name string) *corev1.ServiceAccount {
	for _, obj := range *objs {
		if sa, ok := obj.(*corev1.ServiceAccount); ok {
			if sa.Name == name && sa.Namespace == namespace {
				return sa
			}
		}
	}
	return nil
}

func (objs *clientObjects) findService(namespace, name string) *corev1.Service {
	for _, obj := range *objs {
		if svc, ok := obj.(*corev1.Service); ok {
			if svc.Name == name && svc.Namespace == namespace {
				return svc
			}
		}
	}
	return nil
}

func (objs *clientObjects) findConfigMap(namespace, name string) *corev1.ConfigMap {
	for _, obj := range *objs {
		if cm, ok := obj.(*corev1.ConfigMap); ok {
			if cm.Name == name && cm.Namespace == namespace {
				return cm
			}
		}
	}
	return nil
}

func (objs *clientObjects) getEnvoyConfig(namespace, name string) *testBootstrap {
	cm := objs.findConfigMap(namespace, name).Data
	var bootstrapCfg testBootstrap
	err := protoutils.UnmarshalYAML([]byte(cm["envoy.yaml"]), &bootstrapCfg)
	ExpectWithOffset(1, err).ToNot(HaveOccurred())
	return &bootstrapCfg
}

var _ = Describe("Deployer", func() {
	const (
		defaultNamespace = "default"
	)
	var (
		d *deployer.Deployer

		// Note that this is NOT meant to reflect the actual defaults defined in install/helm/gloo/templates/43-gatewayparameters.yaml
		defaultGatewayParams = func() *gw2_v1alpha1.GatewayParameters {
			return &gw2_v1alpha1.GatewayParameters{
				TypeMeta: metav1.TypeMeta{
					Kind: gw2_v1alpha1.GatewayParametersGVK.Kind,
					// The parsing expects GROUP/VERSION format in this field
					APIVersion: fmt.Sprintf("%s/%s", gw2_v1alpha1.GatewayParametersGVK.Group, gw2_v1alpha1.GatewayParametersGVK.Version),
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      wellknown.DefaultGatewayParametersName,
					Namespace: defaultNamespace,
					UID:       "1237",
				},
				Spec: gw2_v1alpha1.GatewayParametersSpec{
					EnvironmentType: &gw2_v1alpha1.GatewayParametersSpec_Kube{
						Kube: &gw2_v1alpha1.KubernetesProxyConfig{
							WorkloadType: &gw2_v1alpha1.KubernetesProxyConfig_Deployment{
								Deployment: &gw2_v1alpha1.ProxyDeployment{
									Replicas: &wrapperspb.UInt32Value{Value: 2},
								},
							},
							EnvoyContainer: &gw2_v1alpha1.EnvoyContainer{
								Bootstrap: &gw2_v1alpha1.EnvoyBootstrap{
									LogLevel: &wrapperspb.StringValue{Value: "debug"},
									ComponentLogLevels: map[string]string{
										"router":   "info",
										"listener": "warn",
									},
								},
								Image: &kube.Image{
									Registry:   &wrapperspb.StringValue{Value: "scooby"},
									Repository: &wrapperspb.StringValue{Value: "dooby"},
									Tag:        &wrapperspb.StringValue{Value: "doo"},
									PullPolicy: kube.Image_Always,
								},
							},
							PodTemplate: &kube.Pod{
								ExtraAnnotations: map[string]string{
									"foo": "bar",
								},
								SecurityContext: &v1.PodSecurityContext{
									RunAsUser:  ptr.To(int64(1)),
									RunAsGroup: ptr.To(int64(2)),
								},
							},
							Service: &kube.Service{
								Type:      kube.Service_ClusterIP,
								ClusterIP: &wrapperspb.StringValue{Value: "99.99.99.99"},
								ExtraAnnotations: map[string]string{
									"foo": "bar",
								},
							},
							Stats: &gw2_v1alpha1.StatsConfig{
								Enabled:                 &wrapperspb.BoolValue{Value: true},
								RoutePrefixRewrite:      &wrapperspb.StringValue{Value: "/stats/prometheus"},
								EnableStatsRoute:        &wrapperspb.BoolValue{Value: true},
								StatsRoutePrefixRewrite: &wrapperspb.StringValue{Value: "/stats"},
							},
						},
					},
				},
			}
		}

		selfManagedGatewayParam = func(name string) *gw2_v1alpha1.GatewayParameters {
			return &gw2_v1alpha1.GatewayParameters{
				TypeMeta: metav1.TypeMeta{
					Kind: gw2_v1alpha1.GatewayParametersGVK.Kind,
					// The parsing expects GROUP/VERSION format in this field
					APIVersion: fmt.Sprintf("%s/%s", gw2_v1alpha1.GatewayParametersGVK.Group, gw2_v1alpha1.GatewayParametersGVK.Version),
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: defaultNamespace,
					UID:       "1237",
				},
				Spec: gw2_v1alpha1.GatewayParametersSpec{
					EnvironmentType: &gw2_v1alpha1.GatewayParametersSpec_SelfManaged{},
				},
			}
		}
	)
	Context("special cases", func() {
		var gwc *api.GatewayClass
		BeforeEach(func() {
			gwc = &api.GatewayClass{
				ObjectMeta: metav1.ObjectMeta{
					Name: wellknown.GatewayClassName,
				},
				Spec: api.GatewayClassSpec{
					ControllerName: wellknown.GatewayControllerName,
					ParametersRef: &api.ParametersReference{
						Group:     "gateway.gloo.solo.io",
						Kind:      "GatewayParameters",
						Name:      wellknown.DefaultGatewayParametersName,
						Namespace: ptr.To(api.Namespace(defaultNamespace)),
					},
				},
			}
			var err error

			d, err = deployer.NewDeployer(newFakeClientWithObjs(gwc, defaultGatewayParams()), &deployer.Inputs{
				ControllerName: wellknown.GatewayControllerName,
				Dev:            false,
				ControlPlane: bootstrap.ControlPlane{
					Kube: bootstrap.KubernetesControlPlaneConfig{XdsHost: "something.cluster.local", XdsPort: 1234},
				},
			})
			Expect(err).NotTo(HaveOccurred())
		})

		It("should get gvks", func() {
			gvks, err := d.GetGvksToWatch(context.Background())
			Expect(err).NotTo(HaveOccurred())
			Expect(gvks).NotTo(BeEmpty())
		})

		It("support segmenting by release", func() {
			d1, err := deployer.NewDeployer(newFakeClientWithObjs(gwc, defaultGatewayParams()), &deployer.Inputs{
				ControllerName: wellknown.GatewayControllerName,
				Dev:            false,
				ControlPlane: bootstrap.ControlPlane{
					Kube: bootstrap.KubernetesControlPlaneConfig{XdsHost: "something.cluster.local", XdsPort: 1234},
				},
			})
			Expect(err).NotTo(HaveOccurred())

			d2, err := deployer.NewDeployer(newFakeClientWithObjs(gwc, defaultGatewayParams()), &deployer.Inputs{
				ControllerName: wellknown.GatewayControllerName,
				Dev:            false,
				ControlPlane: bootstrap.ControlPlane{
					Kube: bootstrap.KubernetesControlPlaneConfig{XdsHost: "something.cluster.local", XdsPort: 1234},
				},
			})
			Expect(err).NotTo(HaveOccurred())

			gw1 := &api.Gateway{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "foo",
					Namespace: defaultNamespace,
					UID:       "1235",
				},
				TypeMeta: metav1.TypeMeta{
					Kind:       "Gateway",
					APIVersion: "gateway.solo.io/v1beta1",
				},
				Spec: api.GatewaySpec{
					GatewayClassName: wellknown.GatewayClassName,
				},
			}

			gw2 := &api.Gateway{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "bar",
					Namespace: defaultNamespace,
					UID:       "1235",
				},
				TypeMeta: metav1.TypeMeta{
					Kind:       "Gateway",
					APIVersion: "gateway.solo.io/v1beta1",
				},
				Spec: api.GatewaySpec{
					GatewayClassName: wellknown.GatewayClassName,
				},
			}

			proxyName := func(name string) string {
				return fmt.Sprintf("gloo-proxy-%s", name)
			}
			var objs1, objs2 clientObjects
			objs1, err = d1.GetObjsToDeploy(context.Background(), gw1)
			Expect(err).NotTo(HaveOccurred())
			Expect(objs1).NotTo(BeEmpty())
			Expect(objs1.findDeployment(defaultNamespace, proxyName(gw1.Name))).ToNot(BeNil())
			Expect(objs1.findService(defaultNamespace, proxyName(gw1.Name))).ToNot(BeNil())
			Expect(objs1.findConfigMap(defaultNamespace, proxyName(gw1.Name))).ToNot(BeNil())
			// Expect(objs1.findServiceAccount("default")).ToNot(BeNil())
			objs2, err = d2.GetObjsToDeploy(context.Background(), gw2)
			Expect(err).NotTo(HaveOccurred())
			Expect(objs2).NotTo(BeEmpty())
			Expect(objs2.findDeployment(defaultNamespace, proxyName(gw2.Name))).ToNot(BeNil())
			Expect(objs2.findService(defaultNamespace, proxyName(gw2.Name))).ToNot(BeNil())
			Expect(objs2.findConfigMap(defaultNamespace, proxyName(gw2.Name))).ToNot(BeNil())
			// Expect(objs2.findServiceAccount("default")).ToNot(BeNil())

			for _, obj := range objs1 {
				Expect(obj.GetName()).To(Equal("gloo-proxy-foo"))
			}
			for _, obj := range objs2 {
				Expect(obj.GetName()).To(Equal("gloo-proxy-bar"))
			}
		})
	})

	Context("Single gwc and gw", func() {
		type input struct {
			dInputs        *deployer.Inputs
			gw             *api.Gateway
			defaultGwp     *gw2_v1alpha1.GatewayParameters
			overrideGwp    *gw2_v1alpha1.GatewayParameters
			gwc            *api.GatewayClass
			arbitrarySetup func()
		}

		type expectedOutput struct {
			getObjsErr     error
			newDeployerErr error
			validationFunc func(objs clientObjects, inp *input) error
		}

		var (
			gwpOverrideName       = "default-gateway-params"
			defaultDeployerInputs = func() *deployer.Inputs {
				return &deployer.Inputs{
					ControllerName: wellknown.GatewayControllerName,
					Dev:            false,
					ControlPlane: bootstrap.ControlPlane{
						Kube: bootstrap.KubernetesControlPlaneConfig{XdsHost: "something.cluster.local", XdsPort: 1234},
					},
				}
			}
			istioEnabledDeployerInputs = func() *deployer.Inputs {
				inp := defaultDeployerInputs()
				inp.IstioValues = bootstrap.IstioValues{
					IntegrationEnabled: true,
				}
				return inp
			}
			defaultGateway = func() *api.Gateway {
				return &api.Gateway{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "foo",
						Namespace: defaultNamespace,
						UID:       "1235",
					},
					TypeMeta: metav1.TypeMeta{
						Kind:       "Gateway",
						APIVersion: "gateway.solo.io/v1beta1",
					},
					Spec: api.GatewaySpec{
						GatewayClassName: wellknown.GatewayClassName,
						Listeners: []api.Listener{
							{
								Name: "listener-1",
								Port: 80,
							},
						},
					},
				}
			}
			defaultGatewayClass = func() *api.GatewayClass {
				return &api.GatewayClass{
					TypeMeta: metav1.TypeMeta{},
					ObjectMeta: metav1.ObjectMeta{
						Name: wellknown.GatewayClassName,
					},
					Spec: api.GatewayClassSpec{
						ControllerName: wellknown.GatewayControllerName,
						ParametersRef: &api.ParametersReference{
							Group:     "gateway.gloo.solo.io",
							Kind:      "GatewayParameters",
							Name:      wellknown.DefaultGatewayParametersName,
							Namespace: ptr.To(api.Namespace(defaultNamespace)),
						},
					},
				}
			}
			defaultGatewayParamsOverride = func() *gw2_v1alpha1.GatewayParameters {
				return &gw2_v1alpha1.GatewayParameters{
					TypeMeta: metav1.TypeMeta{
						Kind: gw2_v1alpha1.GatewayParametersGVK.Kind,
						// The parsing expects GROUP/VERSION format in this field
						APIVersion: fmt.Sprintf("%s/%s", gw2_v1alpha1.GatewayParametersGVK.Group, gw2_v1alpha1.GatewayParametersGVK.Version),
					},
					ObjectMeta: metav1.ObjectMeta{
						Name:      gwpOverrideName,
						Namespace: defaultNamespace,
						UID:       "1236",
					},
					Spec: gw2_v1alpha1.GatewayParametersSpec{
						EnvironmentType: &gw2_v1alpha1.GatewayParametersSpec_Kube{
							Kube: &gw2_v1alpha1.KubernetesProxyConfig{
								WorkloadType: &gw2_v1alpha1.KubernetesProxyConfig_Deployment{
									Deployment: &gw2_v1alpha1.ProxyDeployment{
										Replicas: &wrapperspb.UInt32Value{Value: 3},
									},
								},
								EnvoyContainer: &gw2_v1alpha1.EnvoyContainer{
									Bootstrap: &gw2_v1alpha1.EnvoyBootstrap{
										LogLevel: &wrapperspb.StringValue{Value: "debug"},
										ComponentLogLevels: map[string]string{
											"router":   "info",
											"listener": "warn",
										},
									},
									Image: &kube.Image{
										Registry:   &wrapperspb.StringValue{Value: "foo"},
										Repository: &wrapperspb.StringValue{Value: "bar"},
										Tag:        &wrapperspb.StringValue{Value: "bat"},
										PullPolicy: kube.Image_Always,
									},
								},
								PodTemplate: &kube.Pod{
									ExtraAnnotations: map[string]string{
										"foo": "bar",
									},
									SecurityContext: &v1.PodSecurityContext{
										RunAsUser:  ptr.To(int64(3)),
										RunAsGroup: ptr.To(int64(4)),
									},
								},
								Service: &kube.Service{
									Type:      kube.Service_ClusterIP,
									ClusterIP: &wrapperspb.StringValue{Value: "99.99.99.99"},
									ExtraAnnotations: map[string]string{
										"foo": "bar",
									},
								},
							},
						},
					},
				}
			}
			gatewayParamsOverrideWithSds = func() *gw2_v1alpha1.GatewayParameters {
				return &gw2_v1alpha1.GatewayParameters{
					TypeMeta: metav1.TypeMeta{
						Kind: gw2_v1alpha1.GatewayParametersGVK.Kind,
						// The parsing expects GROUP/VERSION format in this field
						APIVersion: fmt.Sprintf("%s/%s", gw2_v1alpha1.GatewayParametersGVK.Group, gw2_v1alpha1.GatewayParametersGVK.Version),
					},
					ObjectMeta: metav1.ObjectMeta{
						Name:      gwpOverrideName,
						Namespace: defaultNamespace,
						UID:       "1236",
					},
					Spec: gw2_v1alpha1.GatewayParametersSpec{
						EnvironmentType: &gw2_v1alpha1.GatewayParametersSpec_Kube{
							Kube: &gw2_v1alpha1.KubernetesProxyConfig{
								SdsContainer: &gw2_v1alpha1.SdsContainer{
									Image: &kube.Image{
										Registry:   &wrapperspb.StringValue{Value: "foo"},
										Repository: &wrapperspb.StringValue{Value: "bar"},
										Tag:        &wrapperspb.StringValue{Value: "baz"},
									},
								},
								Istio: &gw2_v1alpha1.IstioIntegration{
									IstioProxyContainer: &gw2_v1alpha1.IstioContainer{
										Image: &kube.Image{
											Registry:   &wrapperspb.StringValue{Value: "scooby"},
											Repository: &wrapperspb.StringValue{Value: "dooby"},
											Tag:        &wrapperspb.StringValue{Value: "doo"},
										},
										IstioDiscoveryAddress: &wrapperspb.StringValue{Value: "can't"},
										IstioMetaMeshId:       &wrapperspb.StringValue{Value: "be"},
										IstioMetaClusterId:    &wrapperspb.StringValue{Value: "overridden"},
									},
								},
								AiExtension: &gw2_v1alpha1.AiExtension{
									Enabled: knownwrappers.Bool(true),
									Image: &kube.Image{
										Registry:   knownwrappers.String("foo"),
										Repository: knownwrappers.String("bar"),
										Tag:        knownwrappers.String("baz"),
									},
									Ports: []*v1.ContainerPort{
										{
											Name:          ptr.To("foo"),
											ContainerPort: ptr.To[int32](80),
										},
									},
								},
							},
						},
					},
				}
			}
			gatewayParamsOverrideWithoutStats = func() *gw2_v1alpha1.GatewayParameters {
				return &gw2_v1alpha1.GatewayParameters{
					TypeMeta: metav1.TypeMeta{
						Kind: gw2_v1alpha1.GatewayParametersGVK.Kind,
						// The parsing expects GROUP/VERSION format in this field
						APIVersion: fmt.Sprintf("%s/%s", gw2_v1alpha1.GatewayParametersGVK.Group, gw2_v1alpha1.GatewayParametersGVK.Version),
					},
					ObjectMeta: metav1.ObjectMeta{
						Name:      gwpOverrideName,
						Namespace: defaultNamespace,
						UID:       "1236",
					},
					Spec: gw2_v1alpha1.GatewayParametersSpec{
						EnvironmentType: &gw2_v1alpha1.GatewayParametersSpec_Kube{
							Kube: &gw2_v1alpha1.KubernetesProxyConfig{
								Stats: &gw2_v1alpha1.StatsConfig{
									Enabled:          &wrapperspb.BoolValue{Value: false},
									EnableStatsRoute: &wrapperspb.BoolValue{Value: false},
								},
							},
						},
					},
				}
			}
			fullyDefinedGatewayParams = func() *gw2_v1alpha1.GatewayParameters {
				return fullyDefinedGatewayParams(wellknown.DefaultGatewayParametersName, defaultNamespace)
			}
			defaultGatewayWithGatewayParams = func(gwpName string) *api.Gateway {
				gw := defaultGateway()
				gw.Annotations = map[string]string{
					wellknown.GatewayParametersAnnotationName: gwpName,
				}

				return gw
			}
			defaultInput = func() *input {
				return &input{
					dInputs:    defaultDeployerInputs(),
					gw:         defaultGateway(),
					defaultGwp: defaultGatewayParams(),
					gwc:        defaultGatewayClass(),
				}
			}
			defaultDeploymentName     = fmt.Sprintf("gloo-proxy-%s", defaultGateway().Name)
			defaultConfigMapName      = defaultDeploymentName
			defaultServiceName        = defaultDeploymentName
			defaultServiceAccountName = defaultDeploymentName

			validateGatewayParametersPropagation = func(objs clientObjects, gwp *gw2_v1alpha1.GatewayParameters) error {
				expectedGwp := gwp.Spec.GetKube()
				Expect(objs).NotTo(BeEmpty())
				// Check we have Deployment, ConfigMap, ServiceAccount, Service
				Expect(objs).To(HaveLen(4))
				dep := objs.findDeployment(defaultNamespace, defaultDeploymentName)
				Expect(dep).ToNot(BeNil())
				Expect(dep.Spec.Replicas).ToNot(BeNil())
				Expect(*dep.Spec.Replicas).To(Equal(int32(expectedGwp.GetDeployment().Replicas.GetValue())))
				expectedImage := fmt.Sprintf("%s/%s",
					expectedGwp.GetEnvoyContainer().GetImage().GetRegistry().GetValue(),
					expectedGwp.GetEnvoyContainer().GetImage().GetRepository().GetValue(),
				)
				Expect(dep.Spec.Template.Spec.Containers[0].Image).To(ContainSubstring(expectedImage))
				if expectedTag := expectedGwp.GetEnvoyContainer().GetImage().GetTag().GetValue(); expectedTag != "" {
					Expect(dep.Spec.Template.Spec.Containers[0].Image).To(ContainSubstring(":" + expectedTag))
				} else {
					Expect(dep.Spec.Template.Spec.Containers[0].Image).To(ContainSubstring(":" + version.Version))
				}
				Expect(string(dep.Spec.Template.Spec.Containers[0].ImagePullPolicy)).To(Equal(expectedGwp.GetEnvoyContainer().GetImage().GetPullPolicy().String()))
				Expect(dep.Spec.Template.Annotations).To(matchers.ContainMapElements(expectedGwp.GetPodTemplate().GetExtraAnnotations()))
				Expect(dep.Spec.Template.Annotations).To(HaveKeyWithValue("prometheus.io/scrape", "true"))
				Expect(*dep.Spec.Template.Spec.SecurityContext.RunAsUser).To(Equal(expectedGwp.GetPodTemplate().GetSecurityContext().GetRunAsUser()))
				Expect(*dep.Spec.Template.Spec.SecurityContext.RunAsGroup).To(Equal(expectedGwp.GetPodTemplate().GetSecurityContext().GetRunAsGroup()))

				svc := objs.findService(defaultNamespace, defaultServiceName)
				Expect(svc).ToNot(BeNil())
				Expect(svc.GetAnnotations()).ToNot(BeNil())
				Expect(svc.Annotations).To(matchers.ContainMapElements(expectedGwp.GetService().GetExtraAnnotations()))
				Expect(string(svc.Spec.Type)).To(Equal(expectedGwp.GetService().GetType().String()))
				Expect(svc.Spec.ClusterIP).To(Equal(expectedGwp.GetService().GetClusterIP().GetValue()))

				sa := objs.findServiceAccount(defaultNamespace, defaultServiceAccountName)
				Expect(sa).ToNot(BeNil())

				cm := objs.findConfigMap(defaultNamespace, defaultConfigMapName)
				Expect(cm).ToNot(BeNil())

				logLevelsMap := expectedGwp.GetEnvoyContainer().GetBootstrap().GetComponentLogLevels()
				levels := []types.GomegaMatcher{}
				for k, v := range logLevelsMap {
					levels = append(levels, ContainSubstring(fmt.Sprintf("%s:%s", k, v)))
				}

				argsMatchers := []interface{}{
					"--log-level",
					expectedGwp.GetEnvoyContainer().GetBootstrap().GetLogLevel().GetValue(),
					"--component-log-level",
					And(levels...),
				}

				Expect(objs.findDeployment(defaultNamespace, defaultDeploymentName).Spec.Template.Spec.Containers[0].Args).To(ContainElements(
					argsMatchers...,
				))
				return nil
			}
		)
		DescribeTable("create and validate objs", func(inp *input, expected *expectedOutput) {
			checkErr := func(err, expectedErr error) (shouldReturn bool) {
				GinkgoHelper()
				if expectedErr != nil {
					Expect(err).To(MatchError(expectedErr))
					return true
				}
				Expect(err).NotTo(HaveOccurred())
				return false
			}

			// run break-glass setup
			if inp.arbitrarySetup != nil {
				inp.arbitrarySetup()
			}

			// Catch nil objs so the fake client doesn't choke
			gwc := inp.gwc
			if gwc == nil {
				gwc = defaultGatewayClass()
			}

			// default these to empty objects so we can test behavior when one or both
			// resources don't exist
			defaultGwp := inp.defaultGwp
			if defaultGwp == nil {
				defaultGwp = &gw2_v1alpha1.GatewayParameters{}
			}
			overrideGwp := inp.overrideGwp
			if overrideGwp == nil {
				overrideGwp = &gw2_v1alpha1.GatewayParameters{}
			}

			d, err := deployer.NewDeployer(newFakeClientWithObjs(gwc, defaultGwp, overrideGwp), inp.dInputs)
			if checkErr(err, expected.newDeployerErr) {
				return
			}

			objs, err := d.GetObjsToDeploy(context.Background(), inp.gw)
			if checkErr(err, expected.getObjsErr) {
				return
			}

			// handle custom test validation func
			Expect(expected.validationFunc(objs, inp)).NotTo(HaveOccurred())
		},
			Entry("No GatewayParameters falls back on default GatewayParameters", &input{
				dInputs:    defaultDeployerInputs(),
				gw:         defaultGateway(),
				defaultGwp: defaultGatewayParams(),
			}, &expectedOutput{
				validationFunc: func(objs clientObjects, inp *input) error {
					return validateGatewayParametersPropagation(objs, defaultGatewayParams())
				},
			}),
			Entry("GatewayParameters overrides", &input{
				dInputs:     defaultDeployerInputs(),
				gw:          defaultGatewayWithGatewayParams(gwpOverrideName),
				defaultGwp:  defaultGatewayParams(),
				overrideGwp: defaultGatewayParamsOverride(),
			}, &expectedOutput{
				validationFunc: func(objs clientObjects, inp *input) error {
					return validateGatewayParametersPropagation(objs, inp.overrideGwp)
				},
			}),
			Entry("Fully defined GatewayParameters", &input{
				dInputs:    istioEnabledDeployerInputs(),
				gw:         defaultGateway(),
				defaultGwp: fullyDefinedGatewayParams(),
			}, &expectedOutput{
				validationFunc: func(objs clientObjects, inp *input) error {
					expectedGwp := inp.defaultGwp.Spec.GetKube()
					Expect(objs).NotTo(BeEmpty())
					// Check we have Deployment, ConfigMap, ServiceAccount, Service
					Expect(objs).To(HaveLen(4))
					dep := objs.findDeployment(defaultNamespace, defaultDeploymentName)
					Expect(dep).ToNot(BeNil())
					Expect(dep.Spec.Replicas).ToNot(BeNil())
					Expect(*dep.Spec.Replicas).To(Equal(int32(expectedGwp.GetDeployment().Replicas.GetValue())))

					Expect(dep.Spec.Template.Annotations).To(matchers.ContainMapElements(expectedGwp.GetPodTemplate().GetExtraAnnotations()))
					Expect(*dep.Spec.Template.Spec.SecurityContext.RunAsUser).To(Equal(expectedGwp.GetPodTemplate().GetSecurityContext().GetRunAsUser()))

					// assert envoy container
					expectedEnvoyImage := fmt.Sprintf("%s/%s",
						expectedGwp.GetEnvoyContainer().GetImage().GetRegistry().GetValue(),
						expectedGwp.GetEnvoyContainer().GetImage().GetRepository().GetValue(),
					)
					envoyContainer := dep.Spec.Template.Spec.Containers[0]
					Expect(envoyContainer.Image).To(ContainSubstring(expectedEnvoyImage))
					if expectedTag := expectedGwp.GetEnvoyContainer().GetImage().GetTag().GetValue(); expectedTag != "" {
						Expect(envoyContainer.Image).To(ContainSubstring(":" + expectedTag))
					} else {
						Expect(envoyContainer.Image).To(ContainSubstring(":" + version.Version))
					}
					Expect(string(envoyContainer.ImagePullPolicy)).To(Equal(expectedGwp.GetEnvoyContainer().GetImage().GetPullPolicy().String()))
					Expect(envoyContainer.Resources.Limits.Cpu().String()).To(Equal(expectedGwp.GetEnvoyContainer().GetResources().GetLimits()["cpu"]))
					Expect(envoyContainer.Resources.Requests.Cpu().String()).To(Equal(expectedGwp.GetEnvoyContainer().GetResources().GetRequests()["cpu"]))

					// assert sds container
					expectedSdsImage := fmt.Sprintf("%s/%s",
						expectedGwp.GetSdsContainer().GetImage().GetRegistry().GetValue(),
						expectedGwp.GetSdsContainer().GetImage().GetRepository().GetValue(),
					)
					sdsContainer := dep.Spec.Template.Spec.Containers[1]
					Expect(sdsContainer.Image).To(ContainSubstring(expectedSdsImage))
					if expectedTag := expectedGwp.GetSdsContainer().GetImage().GetTag().GetValue(); expectedTag != "" {
						Expect(sdsContainer.Image).To(ContainSubstring(":" + expectedTag))
					} else {
						Expect(sdsContainer.Image).To(ContainSubstring(":" + version.Version))
					}
					Expect(string(sdsContainer.ImagePullPolicy)).To(Equal(expectedGwp.GetSdsContainer().GetImage().GetPullPolicy().String()))
					Expect(*sdsContainer.SecurityContext.RunAsUser).To(Equal(expectedGwp.GetSdsContainer().GetSecurityContext().GetRunAsUser()))
					Expect(sdsContainer.Resources.Limits.Cpu().String()).To(Equal(expectedGwp.GetSdsContainer().GetResources().GetLimits()["cpu"]))
					Expect(sdsContainer.Resources.Requests.Cpu().String()).To(Equal(expectedGwp.GetSdsContainer().GetResources().GetRequests()["cpu"]))
					idx := slices.IndexFunc(sdsContainer.Env, func(e corev1.EnvVar) bool {
						return e.Name == "LOG_LEVEL"
					})
					Expect(idx).ToNot(Equal(-1))
					Expect(sdsContainer.Env[idx].Value).To(Equal(expectedGwp.GetSdsContainer().GetBootstrap().GetLogLevel().GetValue()))

					// assert istio container
					istioExpectedImage := fmt.Sprintf("%s/%s",
						expectedGwp.GetIstio().GetIstioProxyContainer().GetImage().GetRegistry().GetValue(),
						expectedGwp.GetIstio().GetIstioProxyContainer().GetImage().GetRepository().GetValue(),
					)
					istioContainer := dep.Spec.Template.Spec.Containers[2]
					Expect(istioContainer.Image).To(ContainSubstring(istioExpectedImage))
					if expectedTag := expectedGwp.GetIstio().GetIstioProxyContainer().GetImage().GetTag().GetValue(); expectedTag != "" {
						Expect(istioContainer.Image).To(ContainSubstring(":" + expectedTag))
					} else {
						Expect(istioContainer.Image).To(ContainSubstring(":" + version.Version))
					}
					Expect(string(istioContainer.ImagePullPolicy)).To(Equal(expectedGwp.GetIstio().GetIstioProxyContainer().GetImage().GetPullPolicy().String()))
					Expect(*istioContainer.SecurityContext.RunAsUser).To(Equal(expectedGwp.GetIstio().GetIstioProxyContainer().GetSecurityContext().GetRunAsUser()))
					Expect(istioContainer.Resources.Limits.Cpu().String()).To(Equal(expectedGwp.GetIstio().GetIstioProxyContainer().GetResources().GetLimits()["cpu"]))
					Expect(istioContainer.Resources.Requests.Cpu().String()).To(Equal(expectedGwp.GetIstio().GetIstioProxyContainer().GetResources().GetRequests()["cpu"]))
					// TODO: assert on istio args (e.g. log level, istio meta fields, etc)

					// assert AI extension container
					expectedAIExtension := fmt.Sprintf("%s/%s",
						expectedGwp.GetAiExtension().GetImage().GetRegistry().GetValue(),
						expectedGwp.GetAiExtension().GetImage().GetRepository().GetValue(),
					)
					aiExt := dep.Spec.Template.Spec.Containers[3]
					Expect(aiExt.Image).To(ContainSubstring(expectedAIExtension))
					Expect(aiExt.Ports).To(HaveLen(len(expectedGwp.GetAiExtension().GetPorts())))

					// assert Service
					svc := objs.findService(defaultNamespace, defaultServiceName)
					Expect(svc).ToNot(BeNil())
					Expect(svc.GetAnnotations()).ToNot(BeNil())
					Expect(svc.Annotations).To(matchers.ContainMapElements(expectedGwp.GetService().GetExtraAnnotations()))
					Expect(string(svc.Spec.Type)).To(Equal(expectedGwp.GetService().GetType().String()))
					Expect(svc.Spec.ClusterIP).To(Equal(expectedGwp.GetService().GetClusterIP().GetValue()))

					sa := objs.findServiceAccount(defaultNamespace, defaultServiceAccountName)
					Expect(sa).ToNot(BeNil())

					cm := objs.findConfigMap(defaultNamespace, defaultConfigMapName)
					Expect(cm).ToNot(BeNil())

					logLevelsMap := expectedGwp.GetEnvoyContainer().GetBootstrap().GetComponentLogLevels()
					levels := []types.GomegaMatcher{}
					for k, v := range logLevelsMap {
						levels = append(levels, ContainSubstring(fmt.Sprintf("%s:%s", k, v)))
					}

					argsMatchers := []interface{}{
						"--log-level",
						expectedGwp.GetEnvoyContainer().GetBootstrap().GetLogLevel().GetValue(),
						"--component-log-level",
						And(levels...),
					}

					Expect(objs.findDeployment(defaultNamespace, defaultDeploymentName).Spec.Template.Spec.Containers[0].Args).To(ContainElements(
						argsMatchers...,
					))
					return nil
				},
			}),
			Entry("correct deployment with sds and AI extension enabled", &input{
				dInputs:     istioEnabledDeployerInputs(),
				gw:          defaultGatewayWithGatewayParams(gwpOverrideName),
				defaultGwp:  defaultGatewayParams(),
				overrideGwp: gatewayParamsOverrideWithSds(),
			}, &expectedOutput{
				validationFunc: func(objs clientObjects, inp *input) error {
					containers := objs.findDeployment(defaultNamespace, defaultDeploymentName).Spec.Template.Spec.Containers
					Expect(containers).To(HaveLen(4))
					var foundGw, foundSds, foundIstioProxy, foundAIExtension bool
					var sdsContainer, istioProxyContainer corev1.Container
					for _, container := range containers {
						switch container.Name {
						case "sds":
							sdsContainer = container
							foundSds = true
						case "istio-proxy":
							istioProxyContainer = container
							foundIstioProxy = true
						case "gloo-gateway":
							foundGw = true
						case "gloo-ai-extension":
							foundAIExtension = true
						default:
							Fail("unknown container name " + container.Name)
						}
					}
					Expect(foundGw).To(BeTrue())
					Expect(foundSds).To(BeTrue())
					Expect(foundIstioProxy).To(BeTrue())
					Expect(foundAIExtension).To(BeTrue())

					bootstrapCfg := objs.getEnvoyConfig(defaultNamespace, defaultConfigMapName)
					clusters := bootstrapCfg.GetStaticResources().GetClusters()
					Expect(clusters).ToNot(BeNil())
					Expect(clusters).To(ContainElement(HaveField("Name", "gateway_proxy_sds")))

					sdsImg := inp.overrideGwp.Spec.GetKube().GetSdsContainer().GetImage()
					Expect(sdsContainer.Image).To(Equal(fmt.Sprintf("%s/%s:%s", sdsImg.GetRegistry().GetValue(), sdsImg.GetRepository().GetValue(), sdsImg.GetTag().GetValue())))
					istioProxyImg := inp.overrideGwp.Spec.GetKube().GetIstio().GetIstioProxyContainer().GetImage()
					Expect(istioProxyContainer.Image).To(Equal(fmt.Sprintf("%s/%s:%s", istioProxyImg.GetRegistry().GetValue(), istioProxyImg.GetRepository().GetValue(), istioProxyImg.GetTag().GetValue())))

					return nil
				},
			}),
			Entry("no listeners on gateway", &input{
				dInputs: defaultDeployerInputs(),
				gw: &api.Gateway{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "foo",
						Namespace: defaultNamespace,
						UID:       "1235",
					},
					TypeMeta: metav1.TypeMeta{
						Kind:       "Gateway",
						APIVersion: "gateway.solo.io/v1beta1",
					},
					Spec: api.GatewaySpec{
						GatewayClassName: "gloo-gateway",
					},
				},
				defaultGwp: defaultGatewayParams(),
			}, &expectedOutput{
				validationFunc: func(objs clientObjects, inp *input) error {
					Expect(objs).NotTo(BeEmpty())
					return nil
				},
			}),
			Entry("port offset", defaultInput(), &expectedOutput{
				validationFunc: func(objs clientObjects, inp *input) error {
					svc := objs.findService(defaultNamespace, defaultServiceName)
					Expect(svc).NotTo(BeNil())

					port := svc.Spec.Ports[0]
					Expect(port.Port).To(Equal(int32(80)))
					Expect(port.TargetPort.IntVal).To(Equal(int32(8080)))
					return nil
				},
			}),
			Entry("duplicate ports", &input{
				dInputs: defaultDeployerInputs(),
				gw: &api.Gateway{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "foo",
						Namespace: defaultNamespace,
						UID:       "1235",
					},
					TypeMeta: metav1.TypeMeta{
						Kind:       "Gateway",
						APIVersion: "gateway.solo.io/v1beta1",
					},
					Spec: api.GatewaySpec{
						GatewayClassName: "gloo-gateway",
						Listeners: []api.Listener{
							{
								Name: "listener-1",
								Port: 80,
							},
							{
								Name: "listener-2",
								Port: 80,
							},
						},
					},
				},
				defaultGwp: defaultGatewayParams(),
			}, &expectedOutput{
				validationFunc: func(objs clientObjects, inp *input) error {
					svc := objs.findService(defaultNamespace, defaultServiceName)
					Expect(svc).NotTo(BeNil())

					Expect(svc.Spec.Ports).To(HaveLen(1))
					port := svc.Spec.Ports[0]
					Expect(port.Port).To(Equal(int32(80)))
					Expect(port.TargetPort.IntVal).To(Equal(int32(8080)))
					return nil
				},
			}),
			Entry("object owner refs are set", defaultInput(), &expectedOutput{
				validationFunc: func(objs clientObjects, inp *input) error {
					Expect(objs).NotTo(BeEmpty())

					gw := defaultGateway()

					for _, obj := range objs {
						ownerRefs := obj.GetOwnerReferences()
						Expect(ownerRefs).To(HaveLen(1))
						Expect(ownerRefs[0].Name).To(Equal(gw.Name))
						Expect(ownerRefs[0].UID).To(Equal(gw.UID))
						Expect(ownerRefs[0].Kind).To(Equal(gw.Kind))
						Expect(ownerRefs[0].APIVersion).To(Equal(gw.APIVersion))
						Expect(*ownerRefs[0].Controller).To(BeTrue())
					}
					return nil
				},
			}),
			Entry("envoy yaml is valid", defaultInput(), &expectedOutput{
				validationFunc: func(objs clientObjects, inp *input) error {
					gw := defaultGateway()
					Expect(objs).NotTo(BeEmpty())

					cm := objs.findConfigMap(defaultNamespace, defaultConfigMapName)
					Expect(cm).NotTo(BeNil())

					envoyYaml := cm.Data["envoy.yaml"]
					Expect(envoyYaml).NotTo(BeEmpty())

					// make sure it's valid yaml
					var envoyConfig map[string]any
					err := yaml.Unmarshal([]byte(envoyYaml), &envoyConfig)
					Expect(err).NotTo(HaveOccurred(), "envoy config is not valid yaml: %s", envoyYaml)

					// make sure the envoy node metadata looks right
					node := envoyConfig["node"].(map[string]any)
					proxyName := fmt.Sprintf("%s-%s", gw.Namespace, gw.Name)
					Expect(node).To(HaveKeyWithValue("metadata", map[string]any{
						xds.RoleKey: fmt.Sprintf("%s~%s~%s", glooutils.GatewayApiProxyValue, gw.Namespace, proxyName),
					}))

					// make sure the stats listener is enabled
					staticResources := envoyConfig["static_resources"].(map[string]any)
					listeners := staticResources["listeners"].([]interface{})
					var prometheusListener map[string]any
					for _, lis := range listeners {
						lis := lis.(map[string]any)
						lisName := lis["name"]
						if lisName == "prometheus_listener" {
							prometheusListener = lis
							break
						}
					}
					Expect(prometheusListener).NotTo(BeNil())

					return nil
				},
			}),
			Entry("envoy yaml is valid with stats disabled", &input{
				dInputs:     defaultDeployerInputs(),
				gw:          defaultGatewayWithGatewayParams(gwpOverrideName),
				defaultGwp:  defaultGatewayParams(),
				overrideGwp: gatewayParamsOverrideWithoutStats(),
				gwc:         defaultGatewayClass(),
			}, &expectedOutput{
				validationFunc: func(objs clientObjects, inp *input) error {
					gw := defaultGatewayWithGatewayParams(gwpOverrideName)
					Expect(objs).NotTo(BeEmpty())

					cm := objs.findConfigMap(defaultNamespace, defaultConfigMapName)
					Expect(cm).NotTo(BeNil())

					envoyYaml := cm.Data["envoy.yaml"]
					Expect(envoyYaml).NotTo(BeEmpty())

					// make sure it's valid yaml
					var envoyConfig map[string]any
					err := yaml.Unmarshal([]byte(envoyYaml), &envoyConfig)
					Expect(err).NotTo(HaveOccurred(), "envoy config is not valid yaml: %s", envoyYaml)

					// make sure the envoy node metadata looks right
					node := envoyConfig["node"].(map[string]any)
					proxyName := fmt.Sprintf("%s-%s", gw.Namespace, gw.Name)
					Expect(node).To(HaveKeyWithValue("metadata", map[string]any{
						xds.RoleKey: fmt.Sprintf("%s~%s~%s", glooutils.GatewayApiProxyValue, gw.Namespace, proxyName),
					}))

					// make sure the stats listener is enabled
					staticResources := envoyConfig["static_resources"].(map[string]any)
					listeners := staticResources["listeners"].([]interface{})
					var prometheusListener map[string]any
					for _, lis := range listeners {
						lis := lis.(map[string]any)
						lisName := lis["name"]
						if lisName == "prometheus_listener" {
							prometheusListener = lis
							break
						}
					}
					Expect(prometheusListener).To(BeNil())

					return nil
				},
			}),
			Entry("failed to get GatewayParameters", &input{
				dInputs:    defaultDeployerInputs(),
				gw:         defaultGatewayWithGatewayParams("bad-gwp"),
				defaultGwp: defaultGatewayParams(),
			}, &expectedOutput{
				getObjsErr: deployer.GetGatewayParametersError,
			}),
			Entry("nil inputs to NewDeployer", &input{
				dInputs:    nil,
				gw:         defaultGateway(),
				defaultGwp: defaultGatewayParams(),
			}, &expectedOutput{
				newDeployerErr: deployer.NilDeployerInputsErr,
			}),
			Entry("No GatewayParameters override but default is self-managed; should not deploy gateway", &input{
				dInputs:    defaultDeployerInputs(),
				gw:         defaultGateway(),
				defaultGwp: selfManagedGatewayParam(wellknown.DefaultGatewayParametersName),
			}, &expectedOutput{
				validationFunc: func(objs clientObjects, inp *input) error {
					Expect(objs).To(BeEmpty())
					return nil
				},
			}),
			Entry("Self-managed GatewayParameters override; should not deploy gateway", &input{
				dInputs:     defaultDeployerInputs(),
				gw:          defaultGatewayWithGatewayParams("self-managed"),
				defaultGwp:  defaultGatewayParams(),
				overrideGwp: selfManagedGatewayParam("self-managed"),
			}, &expectedOutput{
				validationFunc: func(objs clientObjects, inp *input) error {
					Expect(objs).To(BeEmpty())
					return nil
				},
			}),
		)
	})
})

// initialize a fake controller-runtime client with the given list of objects
func newFakeClientWithObjs(objs ...client.Object) client.Client {
	s := scheme.NewScheme()
	return fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).Build()
}

func fullyDefinedGatewayParams(name, namespace string) *gw2_v1alpha1.GatewayParameters {
	return &gw2_v1alpha1.GatewayParameters{
		TypeMeta: metav1.TypeMeta{
			Kind: gw2_v1alpha1.GatewayParametersGVK.Kind,
			// The parsing expects GROUP/VERSION format in this field
			APIVersion: fmt.Sprintf("%s/%s", gw2_v1alpha1.GatewayParametersGVK.Group, gw2_v1alpha1.GatewayParametersGVK.Version),
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			UID:       "1236",
		},
		Spec: gw2_v1alpha1.GatewayParametersSpec{
			EnvironmentType: &gw2_v1alpha1.GatewayParametersSpec_Kube{
				Kube: &gw2_v1alpha1.KubernetesProxyConfig{
					WorkloadType: &gw2_v1alpha1.KubernetesProxyConfig_Deployment{
						Deployment: &gw2_v1alpha1.ProxyDeployment{
							Replicas: &wrapperspb.UInt32Value{Value: 3},
						},
					},
					EnvoyContainer: &gw2_v1alpha1.EnvoyContainer{
						Bootstrap: &gw2_v1alpha1.EnvoyBootstrap{
							LogLevel: &wrapperspb.StringValue{Value: "debug"},
							ComponentLogLevels: map[string]string{
								"router":   "info",
								"listener": "warn",
							},
						},
						Image: &kube.Image{
							Registry:   &wrapperspb.StringValue{Value: "foo"},
							Repository: &wrapperspb.StringValue{Value: "bar"},
							Tag:        &wrapperspb.StringValue{Value: "bat"},
							PullPolicy: kube.Image_Always,
						},
						SecurityContext: &v1.SecurityContext{
							RunAsUser: ptr.To(int64(111)),
						},
						Resources: &kube.ResourceRequirements{
							Limits:   map[string]string{"cpu": "101m"},
							Requests: map[string]string{"cpu": "103m"},
						},
					},
					SdsContainer: &gw2_v1alpha1.SdsContainer{
						Image: &kube.Image{
							Registry:   &wrapperspb.StringValue{Value: "sds-registry"},
							Repository: &wrapperspb.StringValue{Value: "sds-repository"},
							Tag:        &wrapperspb.StringValue{Value: "sds-tag"},
							Digest:     &wrapperspb.StringValue{Value: "sds-digest"},
							PullPolicy: kube.Image_Always,
						},
						SecurityContext: &v1.SecurityContext{
							RunAsUser: ptr.To(int64(222)),
						},
						Resources: &kube.ResourceRequirements{
							Limits:   map[string]string{"cpu": "201m"},
							Requests: map[string]string{"cpu": "203m"},
						},
						Bootstrap: &gw2_v1alpha1.SdsBootstrap{
							LogLevel: &wrapperspb.StringValue{Value: "debug"},
						},
					},
					PodTemplate: &kube.Pod{
						ExtraAnnotations: map[string]string{
							"pod-anno": "foo",
						},
						ExtraLabels: map[string]string{
							"pod-label": "foo",
						},
						SecurityContext: &v1.PodSecurityContext{
							RunAsUser: ptr.To(int64(333)),
						},
						ImagePullSecrets: []*v1.LocalObjectReference{{
							Name: ptr.To("pod-image-pull-secret"),
						}},
						NodeSelector: map[string]string{
							"pod-node-selector": "foo",
						},
						Affinity: &v1.Affinity{
							NodeAffinity: &v1.NodeAffinity{
								RequiredDuringSchedulingIgnoredDuringExecution: &v1.NodeSelector{
									NodeSelectorTerms: []*v1.NodeSelectorTerm{{
										MatchExpressions: []*v1.NodeSelectorRequirement{{
											Key:      ptr.To("pod-affinity-nodeAffinity-required-expression-key"),
											Operator: ptr.To("pod-affinity-nodeAffinity-required-expression-operator"),
											Values:   []string{"foo"},
										}},
										MatchFields: []*v1.NodeSelectorRequirement{{
											Key:      ptr.To("pod-affinity-nodeAffinity-required-field-key"),
											Operator: ptr.To("pod-affinity-nodeAffinity-required-field-operator"),
											Values:   []string{"foo"},
										}},
									}},
								},
							},
						},
						Tolerations: []*v1.Toleration{{
							Key:               ptr.To("pod-toleration-key"),
							Operator:          ptr.To("pod-toleration-operator"),
							Value:             ptr.To("pod-toleration-value"),
							Effect:            ptr.To("pod-toleration-effect"),
							TolerationSeconds: ptr.To(int64(1)),
						}},
					},
					Service: &kube.Service{
						Type:      kube.Service_ClusterIP,
						ClusterIP: &wrapperspb.StringValue{Value: "99.99.99.99"},
						ExtraAnnotations: map[string]string{
							"service-anno": "foo",
						},
						ExtraLabels: map[string]string{
							"service-label": "foo",
						},
					},
					Istio: &gw2_v1alpha1.IstioIntegration{
						IstioProxyContainer: &gw2_v1alpha1.IstioContainer{
							Image: &kube.Image{
								Registry:   &wrapperspb.StringValue{Value: "istio-registry"},
								Repository: &wrapperspb.StringValue{Value: "istio-repository"},
								Tag:        &wrapperspb.StringValue{Value: "istio-tag"},
								Digest:     &wrapperspb.StringValue{Value: "istio-digest"},
								PullPolicy: kube.Image_Always,
							},
							SecurityContext: &v1.SecurityContext{
								RunAsUser: ptr.To(int64(444)),
							},
							Resources: &kube.ResourceRequirements{
								Limits:   map[string]string{"cpu": "301m"},
								Requests: map[string]string{"cpu": "303m"},
							},
							LogLevel:              &wrapperspb.StringValue{Value: "debug"},
							IstioDiscoveryAddress: &wrapperspb.StringValue{Value: "istioDiscoveryAddress"},
							IstioMetaMeshId:       &wrapperspb.StringValue{Value: "istioMetaMeshId"},
							IstioMetaClusterId:    &wrapperspb.StringValue{Value: "istioMetaClusterId"},
						},
					},
					AiExtension: &gw2_v1alpha1.AiExtension{
						Enabled: knownwrappers.Bool(true),
						Ports: []*v1.ContainerPort{
							{
								Name:          ptr.To("foo"),
								ContainerPort: ptr.To[int32](80),
							},
						},
					},
				},
			},
		},
	}
}

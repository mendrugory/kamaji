// Copyright 2022 Clastix Labs
// SPDX-License-Identifier: Apache-2.0

package konnectivity

import (
	"context"
	"fmt"

	"github.com/pkg/errors"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/pointer"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kamajiv1alpha1 "github.com/clastix/kamaji/api/v1alpha1"
	"github.com/clastix/kamaji/internal/utilities"
)

const (
	konnectivityEgressSelectorConfigurationPath = "/etc/kubernetes/konnectivity/configurations/egress-selector-configuration.yaml"
	konnectivityServerName                      = "konnectivity-server"
	konnectivityServerPath                      = "/run/konnectivity"

	egressSelectorConfigurationVolume  = "egress-selector-configuration"
	konnectivityUDSVolume              = "konnectivity-uds"
	konnectivityServerKubeconfigVolume = "konnectivity-server-kubeconfig"
)

type KubernetesDeploymentResource struct {
	resource *appsv1.Deployment
	Client   client.Client
}

func (r *KubernetesDeploymentResource) ShouldStatusBeUpdated(_ context.Context, tenantControlPlane *kamajiv1alpha1.TenantControlPlane) bool {
	switch {
	case tenantControlPlane.Spec.Addons.Konnectivity == nil && tenantControlPlane.Status.Addons.Konnectivity.Enabled:
		fallthrough
	case tenantControlPlane.Spec.Addons.Konnectivity != nil && !tenantControlPlane.Status.Addons.Konnectivity.Enabled:
		return true
	default:
		return false
	}
}

func (r *KubernetesDeploymentResource) ShouldCleanup(tenantControlPlane *kamajiv1alpha1.TenantControlPlane) bool {
	return tenantControlPlane.Spec.Addons.Konnectivity == nil && tenantControlPlane.Status.Addons.Konnectivity.Enabled
}

func (r *KubernetesDeploymentResource) CleanUp(ctx context.Context, _ *kamajiv1alpha1.TenantControlPlane) (bool, error) {
	logger := log.FromContext(ctx)

	logger.Info("performing clean-up from Deployment of Konnectivity")

	res, err := utilities.CreateOrUpdateWithConflict(ctx, r.Client, r.resource, func() error {
		if found, index := utilities.HasNamedContainer(r.resource.Spec.Template.Spec.Containers, konnectivityServerName); found {
			logger.Info("removing Konnectivity container")

			var containers []corev1.Container

			containers = append(containers, r.resource.Spec.Template.Spec.Containers[:index]...)
			containers = append(containers, r.resource.Spec.Template.Spec.Containers[index+1:]...)

			r.resource.Spec.Template.Spec.Containers = containers
		}

		if found, index := utilities.HasNamedContainer(r.resource.Spec.Template.Spec.Containers, "kube-apiserver"); found {
			argsMap := utilities.ArgsFromSliceToMap(r.resource.Spec.Template.Spec.Containers[index].Args)

			if utilities.ArgsRemoveFlag(argsMap, "--egress-selector-config-file") {
				logger.Info("removing egress selector configuration file from kube-apiserver container")

				r.resource.Spec.Template.Spec.Containers[index].Args = utilities.ArgsFromMapToSlice(argsMap)
			}

			for _, volumeName := range []string{konnectivityUDSVolume, egressSelectorConfigurationVolume} {
				if volumeFound, volumeIndex := utilities.HasNamedVolume(r.resource.Spec.Template.Spec.Volumes, volumeName); volumeFound {
					logger.Info("removing Konnectivity volume " + volumeName)

					var volumes []corev1.Volume

					volumes = append(volumes, r.resource.Spec.Template.Spec.Volumes[:volumeIndex]...)
					volumes = append(volumes, r.resource.Spec.Template.Spec.Volumes[volumeIndex+1:]...)

					r.resource.Spec.Template.Spec.Volumes = volumes
				}
			}

			for _, volumeMountName := range []string{konnectivityUDSVolume, egressSelectorConfigurationVolume, konnectivityServerKubeconfigVolume} {
				if ok, i := utilities.HasNamedVolumeMount(r.resource.Spec.Template.Spec.Containers[index].VolumeMounts, volumeMountName); ok {
					logger.Info("removing Konnectivity volume mount " + volumeMountName)

					var volumesMounts []corev1.VolumeMount

					volumesMounts = append(volumesMounts, r.resource.Spec.Template.Spec.Containers[index].VolumeMounts[:i]...)
					volumesMounts = append(volumesMounts, r.resource.Spec.Template.Spec.Containers[index].VolumeMounts[i+1:]...)

					r.resource.Spec.Template.Spec.Containers[index].VolumeMounts = volumesMounts
				}
			}
		}

		return nil
	})

	return res == controllerutil.OperationResultUpdated, err
}

func (r *KubernetesDeploymentResource) Define(_ context.Context, tenantControlPlane *kamajiv1alpha1.TenantControlPlane) error {
	r.resource = &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      tenantControlPlane.GetName(),
			Namespace: tenantControlPlane.GetNamespace(),
		},
	}

	return nil
}

func (r *KubernetesDeploymentResource) syncContainer(tenantControlPlane *kamajiv1alpha1.TenantControlPlane) {
	found, index := utilities.HasNamedContainer(r.resource.Spec.Template.Spec.Containers, konnectivityServerName)
	if !found {
		r.resource.Spec.Template.Spec.Containers = append(r.resource.Spec.Template.Spec.Containers, corev1.Container{})
		index = len(r.resource.Spec.Template.Spec.Containers) - 1
	}

	r.resource.Spec.Template.Spec.Containers[index].Name = konnectivityServerName
	r.resource.Spec.Template.Spec.Containers[index].Image = fmt.Sprintf("%s:%s", tenantControlPlane.Spec.Addons.Konnectivity.KonnectivityServerSpec.Image, tenantControlPlane.Spec.Addons.Konnectivity.KonnectivityServerSpec.Version)
	r.resource.Spec.Template.Spec.Containers[index].Command = []string{"/proxy-server"}

	args := utilities.ArgsFromSliceToMap(tenantControlPlane.Spec.Addons.Konnectivity.KonnectivityServerSpec.ExtraArgs)

	args["--uds-name"] = fmt.Sprintf("%s/konnectivity-server.socket", konnectivityServerPath)
	args["--cluster-cert"] = "/etc/kubernetes/pki/apiserver.crt"
	args["--cluster-key"] = "/etc/kubernetes/pki/apiserver.key"
	args["--mode"] = "grpc"
	args["--server-port"] = "0"
	args["--agent-port"] = fmt.Sprintf("%d", tenantControlPlane.Spec.Addons.Konnectivity.KonnectivityServerSpec.Port)
	args["--admin-port"] = "8133"
	args["--health-port"] = "8134"
	args["--agent-namespace"] = "kube-system"
	args["--agent-service-account"] = AgentName
	args["--kubeconfig"] = "/etc/kubernetes/konnectivity-server.conf"
	args["--authentication-audience"] = CertCommonName
	args["--server-count"] = fmt.Sprintf("%d", tenantControlPlane.Spec.ControlPlane.Deployment.Replicas)

	r.resource.Spec.Template.Spec.Containers[index].Args = utilities.ArgsFromMapToSlice(args)
	r.resource.Spec.Template.Spec.Containers[index].LivenessProbe = &corev1.Probe{
		InitialDelaySeconds: 30,
		TimeoutSeconds:      60,
		PeriodSeconds:       10,
		SuccessThreshold:    1,
		FailureThreshold:    3,
		ProbeHandler: corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{
				Path:   "/healthz",
				Port:   intstr.FromInt(8134),
				Scheme: corev1.URISchemeHTTP,
			},
		},
	}
	r.resource.Spec.Template.Spec.Containers[index].Ports = []corev1.ContainerPort{
		{
			Name:          "agentport",
			ContainerPort: tenantControlPlane.Spec.Addons.Konnectivity.KonnectivityServerSpec.Port,
			Protocol:      corev1.ProtocolTCP,
		},
		{
			Name:          "adminport",
			ContainerPort: 8133,
			Protocol:      corev1.ProtocolTCP,
		},
		{
			Name:          "healthport",
			ContainerPort: 8134,
			Protocol:      corev1.ProtocolTCP,
		},
	}
	r.resource.Spec.Template.Spec.Containers[index].VolumeMounts = []corev1.VolumeMount{
		{
			Name:      "etc-kubernetes-pki",
			MountPath: "/etc/kubernetes/pki",
			ReadOnly:  true,
		},
		{
			Name:      "konnectivity-server-kubeconfig",
			MountPath: "/etc/kubernetes/konnectivity-server.conf",
			SubPath:   "konnectivity-server.conf",
			ReadOnly:  true,
		},
		{
			Name:      konnectivityUDSVolume,
			MountPath: konnectivityServerPath,
			ReadOnly:  false,
		},
	}
	r.resource.Spec.Template.Spec.Containers[index].ImagePullPolicy = corev1.PullAlways
	r.resource.Spec.Template.Spec.Containers[index].Resources = corev1.ResourceRequirements{
		Limits:   nil,
		Requests: nil,
	}

	if resources := tenantControlPlane.Spec.Addons.Konnectivity.KonnectivityServerSpec.Resources; resources != nil {
		r.resource.Spec.Template.Spec.Containers[index].Resources.Limits = resources.Limits
		r.resource.Spec.Template.Spec.Containers[index].Resources.Requests = resources.Requests
	}
}

func (r *KubernetesDeploymentResource) mutate(_ context.Context, tenantControlPlane *kamajiv1alpha1.TenantControlPlane) controllerutil.MutateFn {
	return func() (err error) {
		// If konnectivity is disabled, no operation is required:
		// removal of the container will be performed by clean-up.
		if tenantControlPlane.Spec.Addons.Konnectivity == nil {
			return nil
		}

		if len(r.resource.Spec.Template.Spec.Containers) == 0 {
			return fmt.Errorf("the Deployment resource is not ready to be mangled for Konnectivity server enrichment")
		}

		r.syncContainer(tenantControlPlane)

		if err = r.patchKubeAPIServerContainer(); err != nil {
			return errors.Wrap(err, "cannot sync patch kube-apiserver container")
		}

		r.syncVolumes(tenantControlPlane)

		return nil
	}
}

func (r *KubernetesDeploymentResource) CreateOrUpdate(ctx context.Context, tenantControlPlane *kamajiv1alpha1.TenantControlPlane) (controllerutil.OperationResult, error) {
	return utilities.CreateOrUpdateWithConflict(ctx, r.Client, r.resource, r.mutate(ctx, tenantControlPlane))
}

func (r *KubernetesDeploymentResource) GetName() string {
	return "konnectivity-deployment"
}

func (r *KubernetesDeploymentResource) UpdateTenantControlPlaneStatus(_ context.Context, tenantControlPlane *kamajiv1alpha1.TenantControlPlane) error {
	tenantControlPlane.Status.Addons.Konnectivity.Enabled = tenantControlPlane.Spec.Addons.Konnectivity != nil

	return nil
}

func (r *KubernetesDeploymentResource) patchKubeAPIServerContainer() error {
	// Patching VolumesMounts
	found, index := false, 0

	found, index = utilities.HasNamedContainer(r.resource.Spec.Template.Spec.Containers, "kube-apiserver")
	if !found {
		return fmt.Errorf("missing kube-apiserver container, cannot patch arguments")
	}
	// Adding the egress selector config file flag
	args := utilities.ArgsFromSliceToMap(r.resource.Spec.Template.Spec.Containers[index].Args)

	utilities.ArgsAddFlagValue(args, "--egress-selector-config-file", konnectivityEgressSelectorConfigurationPath)

	r.resource.Spec.Template.Spec.Containers[index].Args = utilities.ArgsFromMapToSlice(args)

	vFound, vIndex := false, 0
	// Patching the volume mounts
	if vFound, vIndex = utilities.HasNamedVolumeMount(r.resource.Spec.Template.Spec.Containers[index].VolumeMounts, konnectivityUDSVolume); !vFound {
		r.resource.Spec.Template.Spec.Containers[index].VolumeMounts = append(r.resource.Spec.Template.Spec.Containers[index].VolumeMounts, corev1.VolumeMount{})
		vIndex = len(r.resource.Spec.Template.Spec.Containers[index].VolumeMounts) - 1
	}

	r.resource.Spec.Template.Spec.Containers[index].VolumeMounts[vIndex].Name = konnectivityUDSVolume
	r.resource.Spec.Template.Spec.Containers[index].VolumeMounts[vIndex].ReadOnly = false
	r.resource.Spec.Template.Spec.Containers[index].VolumeMounts[vIndex].MountPath = konnectivityServerPath

	if vFound, vIndex = utilities.HasNamedVolumeMount(r.resource.Spec.Template.Spec.Containers[index].VolumeMounts, egressSelectorConfigurationVolume); !vFound {
		r.resource.Spec.Template.Spec.Containers[index].VolumeMounts = append(r.resource.Spec.Template.Spec.Containers[index].VolumeMounts, corev1.VolumeMount{})
		vIndex = len(r.resource.Spec.Template.Spec.Containers[index].VolumeMounts) - 1
	}

	r.resource.Spec.Template.Spec.Containers[index].VolumeMounts[vIndex].Name = egressSelectorConfigurationVolume
	r.resource.Spec.Template.Spec.Containers[index].VolumeMounts[vIndex].ReadOnly = false
	r.resource.Spec.Template.Spec.Containers[index].VolumeMounts[vIndex].MountPath = "/etc/kubernetes/konnectivity/configurations"

	return nil
}

func (r *KubernetesDeploymentResource) syncVolumes(tenantControlPlane *kamajiv1alpha1.TenantControlPlane) {
	found, index := false, 0
	// Defining volumes for the UDS socket
	found, index = utilities.HasNamedVolume(r.resource.Spec.Template.Spec.Volumes, konnectivityUDSVolume)
	if !found {
		r.resource.Spec.Template.Spec.Volumes = append(r.resource.Spec.Template.Spec.Volumes, corev1.Volume{})
		index = len(r.resource.Spec.Template.Spec.Volumes) - 1
	}

	r.resource.Spec.Template.Spec.Volumes[index].Name = konnectivityUDSVolume
	r.resource.Spec.Template.Spec.Volumes[index].VolumeSource = corev1.VolumeSource{
		EmptyDir: &corev1.EmptyDirVolumeSource{
			Medium: "Memory",
		},
	}
	// Defining volumes for the egress selector configuration
	found, index = utilities.HasNamedVolume(r.resource.Spec.Template.Spec.Volumes, egressSelectorConfigurationVolume)
	if !found {
		r.resource.Spec.Template.Spec.Volumes = append(r.resource.Spec.Template.Spec.Volumes, corev1.Volume{})
		index = len(r.resource.Spec.Template.Spec.Volumes) - 1
	}

	r.resource.Spec.Template.Spec.Volumes[index].Name = egressSelectorConfigurationVolume
	r.resource.Spec.Template.Spec.Volumes[index].VolumeSource = corev1.VolumeSource{
		ConfigMap: &corev1.ConfigMapVolumeSource{
			LocalObjectReference: corev1.LocalObjectReference{
				Name: tenantControlPlane.Status.Addons.Konnectivity.ConfigMap.Name,
			},
			DefaultMode: pointer.Int32(420),
		},
	}
	// Defining volume for the Konnectivity kubeconfig
	found, index = utilities.HasNamedVolume(r.resource.Spec.Template.Spec.Volumes, konnectivityServerKubeconfigVolume)
	if !found {
		r.resource.Spec.Template.Spec.Volumes = append(r.resource.Spec.Template.Spec.Volumes, corev1.Volume{})
		index = len(r.resource.Spec.Template.Spec.Volumes) - 1
	}

	r.resource.Spec.Template.Spec.Volumes[index].Name = konnectivityServerKubeconfigVolume
	r.resource.Spec.Template.Spec.Volumes[index].VolumeSource = corev1.VolumeSource{
		Secret: &corev1.SecretVolumeSource{
			SecretName:  tenantControlPlane.Status.Addons.Konnectivity.Kubeconfig.SecretName,
			DefaultMode: pointer.Int32(420),
		},
	}
}

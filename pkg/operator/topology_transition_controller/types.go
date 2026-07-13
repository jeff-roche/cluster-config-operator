package topology_transition_controller

import (
	"fmt"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
	operatorv1listers "github.com/openshift/client-go/operator/listers/operator/v1"
	"github.com/openshift/library-go/pkg/operator/v1helpers"
	corev1listers "k8s.io/client-go/listers/core/v1"
)

const (
	// transitionAvailableConditionPrefix is used to build per-transition
	// condition types: TopologyTransition_<Name>_Available
	transitionAvailableConditionPrefix = "TopologyTransition_"
	transitionAvailableConditionSuffix = "_Available"
	transitionProgressingCondition     = "TopologyTransitionControllerProgressing"
	upgradeableCondition               = "TopologyTransitionControllerUpgradeable"
	reasonTopologyTransitionInProgress = "TopologyTransitionInProgress"
)

// TransitionValidatorFunc defines validation functions for transitions
type TransitionValidatorFunc func() error

// TransitionDescriptor describes a topology transition with its source/target
// state, per-transition validation functions, and a status updater. From
// matches against the current Infrastructure status, To matches against the
// desired Infrastructure spec. Zero-value fields act as wildcards in both
// matchers.
type TransitionDescriptor struct {
	Name         string
	From         configv1.InfrastructureStatus
	To           configv1.InfrastructureStatus
	Validators   []TransitionValidatorFunc
	UpdateStatus func(infra *configv1.Infrastructure)
}

// buildSupportedTransitions returns the set of permitted topology transitions
// with validators wired to the necessary tools for accessing cluster information.
func buildSupportedTransitions(nodeLister corev1listers.NodeLister, etcdConfigMapLister corev1listers.ConfigMapNamespaceLister, etcdLister operatorv1listers.EtcdLister) []TransitionDescriptor {
	return []TransitionDescriptor{
		// SNO to HA Compact on platformType: None
		{
			Name: "SNOtoHACompact",
			From: configv1.InfrastructureStatus{
				ControlPlaneTopology:   configv1.SingleReplicaTopologyMode,
				InfrastructureTopology: configv1.SingleReplicaTopologyMode,
				PlatformStatus:         &configv1.PlatformStatus{Type: configv1.NonePlatformType},
			},
			To: configv1.InfrastructureStatus{
				ControlPlaneTopology:   configv1.HighlyAvailableTopologyMode,
				InfrastructureTopology: configv1.HighlyAvailableTopologyMode,
				PlatformStatus:         &configv1.PlatformStatus{Type: configv1.NonePlatformType},
			},
			Validators: []TransitionValidatorFunc{
				validateControlPlaneNodeCount(3, nodeLister),
				validateExactInfrastructureNodeCount(0, nodeLister),
				validateControlPlaneNodesSchedulable(3, nodeLister),
				validateControlPlaneNodesReady(3, nodeLister),
				validateEtcdQuorum(etcdLister),
				validateEtcdNotProgressing(etcdLister),
				validateEtcdVotingMembers(3, etcdConfigMapLister),
			},
			UpdateStatus: func(infra *configv1.Infrastructure) {
				infra.Status.ControlPlaneTopology = configv1.HighlyAvailableTopologyMode
				infra.Status.InfrastructureTopology = configv1.HighlyAvailableTopologyMode
			},
		},
	}
}

// matchesStatus returns true if every non-zero field in descriptor equals
// the corresponding field in actual. Zero-value fields are skipped.
func matchesStatus(descriptor, actual configv1.InfrastructureStatus) bool {
	if descriptor.ControlPlaneTopology != "" && descriptor.ControlPlaneTopology != actual.ControlPlaneTopology {
		return false
	}
	if descriptor.InfrastructureTopology != "" && descriptor.InfrastructureTopology != actual.InfrastructureTopology {
		return false
	}
	if descriptor.PlatformStatus != nil && descriptor.PlatformStatus.Type != "" {
		if actual.PlatformStatus == nil || descriptor.PlatformStatus.Type != actual.PlatformStatus.Type {
			return false
		}
	}
	return true
}

// matchesSpec returns true if every non-zero field in descriptor equals
// the corresponding field in actual. Zero-value fields are skipped.
func matchesSpec(descriptor configv1.InfrastructureStatus, actual configv1.InfrastructureSpec) bool {
	// Control Plane Topology Check
	if descriptor.ControlPlaneTopology != "" && descriptor.ControlPlaneTopology != actual.ControlPlaneTopology {
		return false
	}

	// Platform Type Check
	if descriptor.PlatformStatus != nil && descriptor.PlatformStatus.Type != "" {
		if descriptor.PlatformStatus.Type != actual.PlatformSpec.Type {
			return false
		}
	}

	return true
}

// findTransition returns the TransitionDescriptor matching the current
// Infrastructure state, or an error if no supported transition matches.
func findTransition(infra *configv1.Infrastructure, transitions []TransitionDescriptor) (*TransitionDescriptor, error) {
	for i := range transitions {
		transition := &transitions[i]
		if matchesStatus(transition.From, infra.Status) && matchesSpec(transition.To, infra.Spec) {
			return transition, nil
		}
	}
	platformType := configv1.PlatformType("Unknown")
	if infra.Status.PlatformStatus != nil {
		platformType = infra.Status.PlatformStatus.Type
	}
	return nil, fmt.Errorf("transition from {controlPlane=%s, infrastructure=%s, platform=%s} to {controlPlane=%s} is not supported",
		infra.Status.ControlPlaneTopology, infra.Status.InfrastructureTopology,
		platformType, infra.Spec.ControlPlaneTopology)
}

// removeConditionFn returns an UpdateStatusFunc that removes the
// condition with the given type from the operator status.
func removeConditionFn(condType string) v1helpers.UpdateStatusFunc {
	return func(oldStatus *operatorv1.OperatorStatus) error {
		v1helpers.RemoveOperatorCondition(&oldStatus.Conditions, condType)
		return nil
	}
}

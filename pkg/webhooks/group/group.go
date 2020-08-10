package group

import (
	"encoding/json"
	"net/http"
	"regexp"
	"sync"
	"time"

	responsehelper "github.com/openshift/managed-cluster-validating-webhooks/pkg/helpers"
	"github.com/openshift/managed-cluster-validating-webhooks/pkg/webhooks/utils"
	"k8s.io/api/admission/v1beta1"
	admissionregv1 "k8s.io/api/admissionregistration/v1"
	"k8s.io/apimachinery/pkg/runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/runtime/log"
	admissionctl "sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// GroupWebhook validates a (user.openshift.io) Group change
type GroupWebhook struct {
	mu sync.Mutex
	s  runtime.Scheme
}

// GroupRequest represents a fragment of the data sent as part as part of
// the request
type groupRequest struct {
	Metadata struct {
		Name              string    `json:"name"`
		Namespace         string    `json:"namespace"`
		UID               string    `json:"uid"`
		CreationTimestamp time.Time `json:"creationTimestamp"`
	} `json:"metadata"`
	Users []string `json:"users"`
}

const (
	WebhookName     string = "group-validation"
	protectedGroups string = `(^osd.*|^dedicated-admins$|^cluster-admins$|^layered-cs-sre-admins$)`
)

var (
	log               = logf.Log.WithName(WebhookName)
	protectedGroupsRe = regexp.MustCompile(protectedGroups)
	clusterAdminUsers = []string{"kube:admin", "system:admin"}
	adminGroups       = []string{"osd-sre-admins", "osd-sre-cluster-admins"}

	scope = admissionregv1.ClusterScope
	rules = []admissionregv1.RuleWithOperations{
		{
			Operations: []admissionregv1.OperationType{"UPDATE", "CREATE", "DELETE"},
			Rule: admissionregv1.Rule{
				APIGroups:   []string{"user.openshift.io"},
				APIVersions: []string{"*"},
				Resources:   []string{"groups"},
				Scope:       &scope,
			},
		},
	}
)

// TimeoutSeconds implements Webhook interface
func (s *GroupWebhook) TimeoutSeconds() int32 { return 2 }

// MatchPolicy implements Webhook interface
func (s *GroupWebhook) MatchPolicy() admissionregv1.MatchPolicyType { return admissionregv1.Equivalent }

// Name implements Webhook interface
func (s *GroupWebhook) Name() string { return WebhookName }

// FailurePolicy implements Webhook interface
func (s *GroupWebhook) FailurePolicy() admissionregv1.FailurePolicyType { return admissionregv1.Ignore }

// Rules implements Webhook interface
func (s *GroupWebhook) Rules() []admissionregv1.RuleWithOperations { return rules }

// GetURI implements Webhook interface
func (s *GroupWebhook) GetURI() string { return "/group-validation" }

// SideEffects implements Webhook interface
func (s *GroupWebhook) SideEffects() admissionregv1.SideEffectClass {
	return admissionregv1.SideEffectClassNone
}

// Is the request authorized?
func (s *GroupWebhook) authorized(request admissionctl.Request) admissionctl.Response {
	var ret admissionctl.Response
	// Cluster admins can do anything
	if utils.SliceContains(request.AdmissionRequest.UserInfo.Username, clusterAdminUsers) {
		ret = admissionctl.Allowed("Cluster admins may access")
		ret.UID = request.AdmissionRequest.UID
		return ret
	}
	group := &groupRequest{}
	var err error
	if len(request.OldObject.Raw) > 0 {
		err = json.Unmarshal(request.OldObject.Raw, group)
	} else {
		err = json.Unmarshal(request.Object.Raw, group)
	}
	if err != nil {
		ret = admissionctl.Errored(http.StatusBadRequest, err)
		ret.UID = request.AdmissionRequest.UID
		return ret
	}
	if protectedGroupsRe.Match([]byte(group.Metadata.Name)) {
		// protected group trying to be accessed, so let's check
		for _, usersgroup := range request.AdmissionRequest.UserInfo.Groups {
			// are they an admin?
			if utils.SliceContains(usersgroup, adminGroups) {
				ret = admissionctl.Allowed("Admin may access protected group")
				ret.UID = request.AdmissionRequest.UID
				return ret
			}
		}
		log.Info("Denying access", "request", request.AdmissionRequest)
		ret = admissionctl.Denied("May not access protected group")
		ret.UID = request.AdmissionRequest.UID
		return ret
	}
	// it isn't protected, so let's not be bothered
	ret = admissionctl.Allowed("RBAC allowed")
	ret.UID = request.AdmissionRequest.UID
	return ret
}

// Validate - Make sure we're working with a well-formed Admission Request object
func (s *GroupWebhook) Validate(req admissionctl.Request) bool {
	valid := true
	valid = valid && (req.UserInfo.Username != "")
	valid = valid && (req.Kind.Kind == "Group")

	return valid
}

// HandleRequest Decide if the incoming request is allowed
// Based on https://github.com/openshift/managed-cluster-validating-webhooks/blob/33aae59f588643fb8d1fe19cea9572c759586dd6/src/webhook/group_validation.py
func (s *GroupWebhook) HandleRequest(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	request, _, err := utils.ParseHTTPRequest(r)
	if err != nil {
		log.Error(err, "Error parsing HTTP Request Body")
		responsehelper.SendResponse(w, admissionctl.Errored(http.StatusBadRequest, err))
		return
	}
	// Is this a valid request?
	if !s.Validate(request) {
		responsehelper.SendResponse(w, admissionctl.Errored(http.StatusBadRequest, err))
		return
	}
	// should the request be authorized?
	responsehelper.SendResponse(w, s.authorized(request))
}

// NewWebhook creates a new webhook
func NewWebhook() *GroupWebhook {
	scheme := runtime.NewScheme()
	v1beta1.AddToScheme(scheme)

	return &GroupWebhook{
		s: *scheme,
	}
}

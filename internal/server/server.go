package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	sandboxv1alpha1 "github.com/mNi-Cloud/cloud-api-adaptor-provider-kubernetes/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

type Config struct {
	Address         string
	Namespace       string
	Token           string
	TokenSecretName string
	ClassName       string
}

const (
	AccessSecretLabel    = "sandbox.caa.mnicloud.jp/access"
	AccessSecretTokenKey = "token"
	AccessSecretClassKey = "className"
)

type authorizedClassContextKey struct{}

type Server struct {
	client client.Client
	config Config
	server *http.Server
}

type createSandboxRequest struct {
	Name      string `json:"name"`
	SandboxID string `json:"sandboxID"`
	UserData  string `json:"userData"`
	VCPUs     int64  `json:"vcpus,omitempty"`
	MemoryMiB int64  `json:"memoryMiB,omitempty"`
	Arch      string `json:"arch,omitempty"`
}

type sandboxResponse struct {
	ID   string   `json:"id"`
	Name string   `json:"name"`
	IPs  []string `json:"ips"`
}

func New(kubeClient client.Client, config Config) (*Server, error) {
	if config.Address == "" {
		config.Address = ":8080"
	}
	if config.TokenSecretName == "" {
		config.TokenSecretName = "pod-sandbox-api"
	}
	if config.Namespace == "" || config.ClassName == "" {
		return nil, errors.New("namespace and class name are required")
	}
	s := &Server{client: kubeClient, config: config}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/config", s.authorize(s.getConfig))
	mux.HandleFunc("POST /v1/sandboxes", s.authorize(s.createSandbox))
	mux.HandleFunc("DELETE /v1/sandboxes/{id}", s.authorize(s.deleteSandbox))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	s.server = &http.Server{Addr: config.Address, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	return s, nil
}

type providerConfigResponse struct {
	NetworkMTU int32 `json:"networkMTU"`
}

func (s *Server) getConfig(w http.ResponseWriter, r *http.Request) {
	sandboxClass, err := s.getClass(r.Context(), authorizedClass(r.Context()))
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	networkMTU := sandboxClass.Spec.NetworkMTU
	if networkMTU == 0 {
		networkMTU = 1500
	}
	writeJSON(w, http.StatusOK, providerConfigResponse{NetworkMTU: networkMTU})
}

func (s *Server) getClass(ctx context.Context, className string) (*sandboxv1alpha1.PodSandboxClass, error) {
	var sandboxClass sandboxv1alpha1.PodSandboxClass
	if err := s.client.Get(ctx, types.NamespacedName{Name: className}, &sandboxClass); err != nil {
		return nil, fmt.Errorf("get PodSandboxClass %q: %w", className, err)
	}
	return &sandboxClass, nil
}

func (s *Server) Start(ctx context.Context) error {
	if s.config.Token == "" {
		if err := s.ensureTokenSecret(ctx); err != nil {
			return err
		}
	}
	errCh := make(chan error, 1)
	go func() { errCh <- s.server.ListenAndServe() }()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return s.server.Shutdown(shutdownCtx)
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func (*Server) NeedLeaderElection() bool { return true }

func (s *Server) authorize(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		className, err := s.authorizedClass(r.Context(), strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if err != nil {
			http.Error(w, "authorization is unavailable", http.StatusServiceUnavailable)
			return
		}
		if className == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), authorizedClassContextKey{}, className)))
	}
}

func (s *Server) authorizedClass(ctx context.Context, got string) (string, error) {
	expected, err := s.token(ctx)
	if err != nil {
		return "", err
	}
	if tokenMatches(got, expected) {
		return s.config.ClassName, nil
	}

	var accesses corev1.SecretList
	if err := s.client.List(ctx, &accesses, client.InNamespace(s.config.Namespace), client.MatchingLabels{AccessSecretLabel: "true"}); err != nil {
		return "", fmt.Errorf("list Pod Sandbox API access secrets: %w", err)
	}
	matchedClass := ""
	for i := range accesses.Items {
		candidate := string(accesses.Items[i].Data[AccessSecretTokenKey])
		if !tokenMatches(got, candidate) {
			continue
		}
		className := string(accesses.Items[i].Data[AccessSecretClassKey])
		if className == "" || (matchedClass != "" && matchedClass != className) {
			return "", errors.New("pod sandbox API access secret is invalid")
		}
		matchedClass = className
	}
	return matchedClass, nil
}

func tokenMatches(got, expected string) bool {
	return got != "" && len(got) == len(expected) && subtle.ConstantTimeCompare([]byte(got), []byte(expected)) == 1
}

func authorizedClass(ctx context.Context) string {
	className, _ := ctx.Value(authorizedClassContextKey{}).(string)
	return className
}

func (s *Server) token(ctx context.Context) (string, error) {
	if s.config.Token != "" {
		return s.config.Token, nil
	}
	var secret corev1.Secret
	if err := s.client.Get(ctx, types.NamespacedName{Namespace: s.config.Namespace, Name: s.config.TokenSecretName}, &secret); err != nil {
		return "", err
	}
	token := string(secret.Data["token"])
	if token == "" {
		return "", errors.New("pod sandbox API token is empty")
	}
	return token, nil
}

func (s *Server) ensureTokenSecret(ctx context.Context) error {
	key := types.NamespacedName{Namespace: s.config.Namespace, Name: s.config.TokenSecretName}
	var existing corev1.Secret
	if err := s.client.Get(ctx, key, &existing); err == nil {
		if len(existing.Data["token"]) == 0 {
			return fmt.Errorf("secret %s/%s has no token key", key.Namespace, key.Name)
		}
		return nil
	} else if !apierrors.IsNotFound(err) {
		return fmt.Errorf("get Pod Sandbox API token secret: %w", err)
	}

	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return fmt.Errorf("generate Pod Sandbox API token: %w", err)
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace},
		Data:       map[string][]byte{"token": []byte(hex.EncodeToString(random))},
		Type:       corev1.SecretTypeOpaque,
	}
	if err := s.client.Create(ctx, secret); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create Pod Sandbox API token secret: %w", err)
	}
	return nil
}

func (s *Server) createSandbox(w http.ResponseWriter, r *http.Request) {
	className := authorizedClass(r.Context())
	if _, err := s.getClass(r.Context(), className); err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	var request createSandboxRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&request); err != nil {
		http.Error(w, "invalid request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if request.SandboxID == "" || request.UserData == "" {
		http.Error(w, "sandboxID and userData are required", http.StatusBadRequest)
		return
	}
	if request.VCPUs <= 0 {
		request.VCPUs = 2
	}
	if request.MemoryMiB <= 0 {
		request.MemoryMiB = 2048
	}
	name := sandboxName(request.SandboxID)
	key := types.NamespacedName{Namespace: s.config.Namespace, Name: name}

	var sandbox sandboxv1alpha1.PodSandbox
	createdHere := false
	completed := false
	defer func() {
		if createdHere && !completed {
			_ = s.client.Delete(context.WithoutCancel(r.Context()), &sandbox)
		}
	}()
	err := s.client.Get(r.Context(), key, &sandbox)
	if err != nil && !apierrors.IsNotFound(err) {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if apierrors.IsNotFound(err) {
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: name + "-userdata", Namespace: s.config.Namespace},
			Immutable:  ptr.To(true),
			Data:       map[string][]byte{"userdata": []byte(request.UserData)},
		}
		if err := s.client.Create(r.Context(), secret); err != nil && !apierrors.IsAlreadyExists(err) {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		sandbox = sandboxv1alpha1.PodSandbox{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: s.config.Namespace},
			Spec: sandboxv1alpha1.PodSandboxSpec{
				SandboxID:         request.SandboxID,
				UserDataSecretRef: corev1.LocalObjectReference{Name: secret.Name},
				ClassName:         className,
				VCPUs:             request.VCPUs,
				MemoryMiB:         request.MemoryMiB,
				Arch:              request.Arch,
			},
		}
		if err := s.client.Create(r.Context(), &sandbox); err != nil {
			_ = s.client.Delete(r.Context(), secret)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		createdHere = true
		if err := controllerutil.SetControllerReference(&sandbox, secret, s.client.Scheme()); err == nil {
			_ = s.client.Update(r.Context(), secret)
		}
	} else if sandbox.Spec.SandboxID != request.SandboxID || sandbox.Spec.ClassName != className {
		http.Error(w, "sandbox identity conflict", http.StatusConflict)
		return
	}

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := s.client.Get(r.Context(), key, &sandbox); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if conditionTrue(sandbox.Status.Conditions, sandboxv1alpha1.ConditionReady) && len(sandbox.Status.IPs) > 0 {
			completed = true
			writeJSON(w, http.StatusCreated, sandboxResponse{ID: sandbox.Name, Name: request.Name, IPs: sandbox.Status.IPs})
			return
		}
		if condition := conditionByType(sandbox.Status.Conditions, sandboxv1alpha1.ConditionReady); condition != nil && condition.Status == metav1.ConditionFalse && condition.Reason == "RunnerFailed" {
			http.Error(w, condition.Message, http.StatusInternalServerError)
			return
		}
		select {
		case <-r.Context().Done():
			http.Error(w, r.Context().Err().Error(), http.StatusGatewayTimeout)
			return
		case <-ticker.C:
		}
	}
}

func (s *Server) deleteSandbox(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" || strings.Contains(id, "/") {
		http.Error(w, "invalid sandbox id", http.StatusBadRequest)
		return
	}
	sandbox := &sandboxv1alpha1.PodSandbox{}
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: id, Namespace: s.config.Namespace}, sandbox); err != nil {
		if apierrors.IsNotFound(err) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if sandbox.Spec.ClassName != authorizedClass(r.Context()) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := s.client.Delete(r.Context(), sandbox); err != nil && !apierrors.IsNotFound(err) {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func sandboxName(sandboxID string) string {
	digest := sha256.Sum256([]byte(sandboxID))
	return "psb-" + hex.EncodeToString(digest[:10])
}

func conditionTrue(conditions []metav1.Condition, conditionType string) bool {
	condition := conditionByType(conditions, conditionType)
	return condition != nil && condition.Status == metav1.ConditionTrue
}

func conditionByType(conditions []metav1.Condition, conditionType string) *metav1.Condition {
	for _, condition := range conditions {
		if condition.Type == conditionType {
			return &condition
		}
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		_ = fmt.Errorf("encode response: %w", err)
	}
}

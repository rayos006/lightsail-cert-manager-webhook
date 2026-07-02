// cert-manager ACME DNS-01 solver webhook for AWS Lightsail DNS.
//
// Lightsail has its own DNS zones (distinct from Route53) with a simplified
// API. cert-manager has no built-in solver, and no maintained community
// webhook — so this one.
//
// Config (set in ClusterIssuer.spec.acme.solvers.dns01.webhook.config):
//   {
//     "region": "us-east-1",                    // Lightsail DNS is only in us-east-1
//     "accessKeyIDSecretRef":     {"name": "lightsail-dns-creds", "key": "access-key-id"},
//     "secretAccessKeySecretRef": {"name": "lightsail-dns-creds", "key": "secret-access-key"}
//   }
//
// The IAM user backing those creds needs at minimum:
//   lightsail:GetDomain
//   lightsail:CreateDomainEntry
//   lightsail:DeleteDomainEntry
//   lightsail:GetDomains
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/lightsail"
	lstypes "github.com/aws/aws-sdk-go-v2/service/lightsail/types"

	extapi "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	cmmeta "github.com/cert-manager/cert-manager/pkg/apis/meta/v1"
	"github.com/cert-manager/cert-manager/pkg/acme/webhook"
	"github.com/cert-manager/cert-manager/pkg/acme/webhook/apis/acme/v1alpha1"
	"github.com/cert-manager/cert-manager/pkg/acme/webhook/cmd"
)

var GroupName = os.Getenv("GROUP_NAME")

func main() {
	if GroupName == "" {
		panic("GROUP_NAME must be specified")
	}
	cmd.RunWebhookServer(GroupName, &lightsailDNSProviderSolver{})
}

type lightsailDNSProviderSolver struct {
	client *kubernetes.Clientset
}

var _ webhook.Solver = (*lightsailDNSProviderSolver)(nil)

// lightsailDNSProviderConfig is decoded from the ClusterIssuer's webhook config.
type lightsailDNSProviderConfig struct {
	// Region has to be us-east-1 today (Lightsail's DNS control plane).
	Region string `json:"region"`

	// AccessKeyIDSecretRef points at a Secret key holding the IAM user's
	// access key id (in the namespace of the ClusterIssuer's solver).
	AccessKeyIDSecretRef cmmeta.SecretKeySelector `json:"accessKeyIDSecretRef"`

	// SecretAccessKeySecretRef same for the secret key.
	SecretAccessKeySecretRef cmmeta.SecretKeySelector `json:"secretAccessKeySecretRef"`
}

func (c *lightsailDNSProviderSolver) Name() string { return "lightsail" }

// Present is called by cert-manager to add the ACME challenge TXT record.
// Must be idempotent — cert-manager may call it multiple times.
func (c *lightsailDNSProviderSolver) Present(ch *v1alpha1.ChallengeRequest) error {
	cli, domain, recordName, err := c.setup(ch)
	if err != nil {
		return err
	}
	ctx := context.Background()

	// If the record with the same name+value already exists, skip.
	existing, err := findEntry(ctx, cli, domain, recordName, ch.Key)
	if err != nil {
		return err
	}
	if existing != nil {
		return nil
	}

	// Lightsail's TXT record target must include surrounding quotes.
	target := fmt.Sprintf("\"%s\"", ch.Key)
	_, err = cli.CreateDomainEntry(ctx, &lightsail.CreateDomainEntryInput{
		DomainName: aws.String(domain),
		DomainEntry: &lstypes.DomainEntry{
			Name:   aws.String(recordName),
			Type:   aws.String("TXT"),
			Target: aws.String(target),
		},
	})
	if err != nil {
		return fmt.Errorf("lightsail CreateDomainEntry %s %s: %w", domain, recordName, err)
	}
	return nil
}

// CleanUp removes the TXT record with our specific value. Other TXT records
// with the same name (concurrent challenges) must be left alone.
func (c *lightsailDNSProviderSolver) CleanUp(ch *v1alpha1.ChallengeRequest) error {
	cli, domain, recordName, err := c.setup(ch)
	if err != nil {
		return err
	}
	ctx := context.Background()

	entry, err := findEntry(ctx, cli, domain, recordName, ch.Key)
	if err != nil {
		return err
	}
	if entry == nil {
		// Nothing to delete — treat as success (idempotency).
		return nil
	}
	_, err = cli.DeleteDomainEntry(ctx, &lightsail.DeleteDomainEntryInput{
		DomainName:  aws.String(domain),
		DomainEntry: entry,
	})
	if err != nil {
		return fmt.Errorf("lightsail DeleteDomainEntry %s %s: %w", domain, recordName, err)
	}
	return nil
}

func (c *lightsailDNSProviderSolver) Initialize(kubeClientConfig *rest.Config, stopCh <-chan struct{}) error {
	cl, err := kubernetes.NewForConfig(kubeClientConfig)
	if err != nil {
		return fmt.Errorf("initializing kube client: %w", err)
	}
	c.client = cl
	return nil
}

// setup decodes the config, fetches AWS creds from the referenced Secret,
// builds a Lightsail client, and computes the domain + record-name for the
// current challenge.
func (c *lightsailDNSProviderSolver) setup(ch *v1alpha1.ChallengeRequest) (*lightsail.Client, string, string, error) {
	cfg, err := loadConfig(ch.Config)
	if err != nil {
		return nil, "", "", err
	}
	if cfg.Region == "" {
		cfg.Region = "us-east-1"
	}

	accessKeyID, err := c.secret(ch.ResourceNamespace, cfg.AccessKeyIDSecretRef)
	if err != nil {
		return nil, "", "", fmt.Errorf("access key: %w", err)
	}
	secretAccessKey, err := c.secret(ch.ResourceNamespace, cfg.SecretAccessKeySecretRef)
	if err != nil {
		return nil, "", "", fmt.Errorf("secret key: %w", err)
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(context.Background(),
		awsconfig.WithRegion(cfg.Region),
		awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(accessKeyID, secretAccessKey, ""),
		),
	)
	if err != nil {
		return nil, "", "", fmt.Errorf("aws config: %w", err)
	}

	// ch.ResolvedZone is e.g. "example.com." — strip trailing dot.
	domain := strings.TrimSuffix(ch.ResolvedZone, ".")
	// ch.ResolvedFQDN is e.g. "_acme-challenge.sub.example.com." — turn into
	// a record name relative to the zone (Lightsail's DomainEntry.Name is FQDN).
	recordName := strings.TrimSuffix(ch.ResolvedFQDN, ".")

	return lightsail.NewFromConfig(awsCfg), domain, recordName, nil
}

// secret pulls a value from a Kubernetes Secret in the given namespace.
func (c *lightsailDNSProviderSolver) secret(ns string, ref cmmeta.SecretKeySelector) (string, error) {
	s, err := c.client.CoreV1().Secrets(ns).Get(context.Background(), ref.Name, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("getting secret %s/%s: %w", ns, ref.Name, err)
	}
	v, ok := s.Data[ref.Key]
	if !ok {
		return "", fmt.Errorf("secret %s/%s has no key %s", ns, ref.Name, ref.Key)
	}
	return string(v), nil
}

// findEntry searches the domain's existing TXT records for one whose value
// matches our key (accounting for the surrounding quotes Lightsail adds).
func findEntry(ctx context.Context, cli *lightsail.Client, domain, recordName, key string) (*lstypes.DomainEntry, error) {
	out, err := cli.GetDomain(ctx, &lightsail.GetDomainInput{
		DomainName: aws.String(domain),
	})
	if err != nil {
		return nil, fmt.Errorf("lightsail GetDomain %s: %w", domain, err)
	}
	quoted := fmt.Sprintf("\"%s\"", key)
	for i := range out.Domain.DomainEntries {
		e := out.Domain.DomainEntries[i]
		if e.Type == nil || *e.Type != "TXT" {
			continue
		}
		if e.Name == nil || *e.Name != recordName {
			continue
		}
		if e.Target != nil && (*e.Target == key || *e.Target == quoted) {
			return &e, nil
		}
	}
	return nil, nil
}

func loadConfig(cfgJSON *extapi.JSON) (lightsailDNSProviderConfig, error) {
	cfg := lightsailDNSProviderConfig{}
	if cfgJSON == nil {
		return cfg, nil
	}
	if err := json.Unmarshal(cfgJSON.Raw, &cfg); err != nil {
		return cfg, fmt.Errorf("decoding solver config: %w", err)
	}
	return cfg, nil
}

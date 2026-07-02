module github.com/rayos006/lightsail-cert-manager-webhook

go 1.23

require (
	github.com/aws/aws-sdk-go-v2 v1.32.6
	github.com/aws/aws-sdk-go-v2/config v1.28.6
	github.com/aws/aws-sdk-go-v2/credentials v1.17.47
	github.com/aws/aws-sdk-go-v2/service/lightsail v1.42.7
	github.com/cert-manager/cert-manager v1.16.5
	k8s.io/apiextensions-apiserver v0.31.4
	k8s.io/apimachinery v0.31.4
	k8s.io/client-go v0.31.4
)

package controller

import (
	"os"
	"strings"

	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
)

const (
	imageBaseIgrisHTTP           = "igris-http"
	imageBaseIgrisRabbitMQ       = "igris-rabbitmq"
	imageBaseEgressRelayRabbitMQ = "egress-relay-rabbitmq"
	imageBaseRecorder            = "recorder"
	imageBaseBeru                = "beru"

	envIgrisHTTPImage           = "IGRIS_HTTP_IMAGE"
	envIgrisRabbitMQImage       = "IGRIS_RABBITMQ_IMAGE"
	envEgressRelayRabbitMQImage = "EGRESS_RELAY_RABBITMQ_IMAGE"
	envRecorderImage            = "RECORDER_IMAGE"
	envBeruImage                = "BERU_IMAGE"
)

func monarchImageTagSuffix() string {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MONARCH_MODE"))) {
	case "dev", "development":
		return ":dev"
	default:
		return ":latest"
	}
}

func resolveHelperImage(base, crOverride, envVar string) string {
	if crOverride != "" {
		return crOverride
	}
	if v := strings.TrimSpace(os.Getenv(envVar)); v != "" {
		return v
	}
	return base + monarchImageTagSuffix()
}

func igrisHTTPImageFor(st *enginev1alpha1.ShadowTest) string {
	cr := ""
	if st.Spec.Igris != nil {
		cr = st.Spec.Igris.Image
	}
	return resolveHelperImage(imageBaseIgrisHTTP, cr, envIgrisHTTPImage)
}

func igrisRabbitMQImageFor(st *enginev1alpha1.ShadowTest) string {
	cr := ""
	if st.Spec.IgrisRabbitMQ != nil {
		cr = st.Spec.IgrisRabbitMQ.Image
	}
	return resolveHelperImage(imageBaseIgrisRabbitMQ, cr, envIgrisRabbitMQImage)
}

func egressRelayRabbitMQImageFor(st *enginev1alpha1.ShadowTest) string {
	cr := ""
	if st.Spec.EgressRelayRabbitMQ != nil {
		cr = st.Spec.EgressRelayRabbitMQ.Image
	}
	return resolveHelperImage(imageBaseEgressRelayRabbitMQ, cr, envEgressRelayRabbitMQImage)
}

func recorderImageFor(st *enginev1alpha1.ShadowTest) string {
	cr := ""
	if st.Spec.Recorder != nil {
		cr = st.Spec.Recorder.Image
	}
	return resolveHelperImage(imageBaseRecorder, cr, envRecorderImage)
}

func beruImageFor(st *enginev1alpha1.ShadowTest) string {
	return resolveHelperImage(imageBaseBeru, "", envBeruImage)
}

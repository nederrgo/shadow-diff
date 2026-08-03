package controller

import (
	"os"
	"strings"

	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
)

const (
	imageRegistryDefault = "ghcr.io/shadow-diff"

	imageBaseIgrisHTTP           = "igris-http"
	imageBaseIgrisRabbitMQ       = "igris-rabbitmq"
	imageBaseEgressRelayRabbitMQ = "egress-relay-rabbitmq"
	imageBaseBeru                = "beru"
	imageBaseShop                = "shop"
	imageBaseShadowSoldier       = "shadow-soldier"

	defaultEnvoyImage = "envoyproxy/envoy:v1.30-latest"

	envIgrisHTTPImage           = "IGRIS_HTTP_IMAGE"
	envIgrisRabbitMQImage       = "IGRIS_RABBITMQ_IMAGE"
	envEgressRelayRabbitMQImage = "EGRESS_RELAY_RABBITMQ_IMAGE"
	envBeruImage                = "BERU_IMAGE"
	envShopImage                = "SHOP_IMAGE"
	envShadowSoldierImage       = "SHADOW_SOLDIER_IMAGE"
	envEnvoyImage               = "ENVOY_IMAGE"
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
	return imageRegistryDefault + "/" + base + monarchImageTagSuffix()
}

// envoyImageFor returns ENVOY_IMAGE when set, otherwise the upstream Envoy default.
func envoyImageFor() string {
	if v := strings.TrimSpace(os.Getenv(envEnvoyImage)); v != "" {
		return v
	}
	return defaultEnvoyImage
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

func beruImageFor(st *enginev1alpha1.ShadowTest) string {
	cr := ""
	if st.Spec.Beru != nil {
		cr = st.Spec.Beru.Image
	}
	return resolveHelperImage(imageBaseBeru, cr, envBeruImage)
}

func shopImageFor(st *enginev1alpha1.ShadowTest) string {
	cr := ""
	if st.Spec.Shop != nil {
		cr = st.Spec.Shop.Image
	}
	return resolveHelperImage(imageBaseShop, cr, envShopImage)
}

func shadowSoldierImageFor(st *enginev1alpha1.ShadowTest) string {
	cr := ""
	if st.Spec.ShadowSoldier != nil {
		cr = st.Spec.ShadowSoldier.Image
	}
	return resolveHelperImage(imageBaseShadowSoldier, cr, envShadowSoldierImage)
}

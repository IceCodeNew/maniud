package docker

import (
	"strings"

	"github.com/IceCodeNew/maniud/internal/domain"
)

const (
	maximumLabelValueBytes = 128
	maniudLabelPrefix      = "io.maniud."
)

func decodeOwnership(labels map[string]string, imageConfig, platformManifest domain.Digest) domain.WorkloadOwnership {
	var conflicting domain.WorkloadOwnership

	if !hasManiudLabel(labels) {
		return domain.WorkloadOwnership{
			Status:           domain.OwnershipUnmanaged,
			Service:          "",
			Transaction:      "",
			DesiredState:     domain.Digest{},
			Reference:        domain.Digest{},
			ImageConfig:      domain.Digest{},
			PlatformManifest: domain.Digest{},
		}
	}

	if !supportedOwnershipLabels(labels) {
		return conflicting
	}

	ownership, valid := requiredOwnership(labels, imageConfig, platformManifest)
	if !valid {
		return conflicting
	}

	return ownership
}

func requiredOwnership(
	labels map[string]string,
	imageConfig domain.Digest,
	platformManifest domain.Digest,
) (domain.WorkloadOwnership, bool) {
	var conflicting domain.WorkloadOwnership

	desiredState, desiredErr := domain.ParseDigest(labels[domain.LabelDesiredStateDigest])
	reference, referenceErr := domain.ParseDigest(labels[domain.LabelReferenceDigest])
	labeledImage, imageErr := domain.ParseDigest(labels[domain.LabelImageConfigDigest])
	labeledManifest, manifestErr := domain.ParseDigest(labels[domain.LabelPlatformManifestDigest])
	service := labels[domain.LabelService]
	transaction := labels[domain.LabelTransaction]

	if desiredErr != nil || referenceErr != nil || imageErr != nil || manifestErr != nil ||
		labeledImage != imageConfig || labeledManifest != platformManifest ||
		!validOwnershipName(service) || !validTransaction(transaction) {
		return conflicting, false
	}

	return domain.WorkloadOwnership{
		Status:           domain.OwnershipManaged,
		Service:          service,
		Transaction:      transaction,
		DesiredState:     desiredState,
		Reference:        reference,
		ImageConfig:      labeledImage,
		PlatformManifest: labeledManifest,
	}, true
}

func hasManiudLabel(labels map[string]string) bool {
	for key := range labels {
		if strings.HasPrefix(key, maniudLabelPrefix) {
			return true
		}
	}

	return false
}

func supportedOwnershipLabels(labels map[string]string) bool {
	required := map[string]bool{
		domain.LabelService:                false,
		domain.LabelTransaction:            false,
		domain.LabelDesiredStateDigest:     false,
		domain.LabelReferenceDigest:        false,
		domain.LabelImageConfigDigest:      false,
		domain.LabelPlatformManifestDigest: false,
	}

	for key := range labels {
		if _, tracked := required[key]; tracked {
			required[key] = true

			continue
		}

		if strings.HasPrefix(key, maniudLabelPrefix) {
			return false
		}
	}

	for _, present := range required {
		if !present {
			return false
		}
	}

	return true
}

func validOwnershipName(value string) bool {
	if len(value) == 0 || len(value) > maximumLabelValueBytes || !alphaNumeric(value[0]) {
		return false
	}

	for index := range value {
		if !ownershipNameByte(value[index]) {
			return false
		}
	}

	return true
}

func ownershipNameByte(value byte) bool {
	return alphaNumeric(value) || value == '.' || value == '_' || value == '-'
}

func validTransaction(value string) bool {
	if len(value) == 0 || len(value) > maximumLabelValueBytes || !alphaNumeric(value[0]) {
		return false
	}

	for index := 1; index < len(value); index++ {
		if value[index] != '-' && !alphaNumeric(value[index]) {
			return false
		}
	}

	return true
}

func alphaNumeric(value byte) bool {
	return lowerAlphaNumeric(value) || value >= 'A' && value <= 'Z'
}

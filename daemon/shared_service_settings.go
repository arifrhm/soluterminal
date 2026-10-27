package main

import (
	"regexp"
)

var (
	openAICompatProfileKeyRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,23}$`)
	openAICompatProviderRe   = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
)

var localOpenAIReasoningEfforts = []string{"none", "low", "medium", "high"}

const (
	releaseTrainDefaultMinLandings        = 6
	releaseTrainDefaultMaxAgeHours        = 12
	releaseTrainDefaultVetoMinutes        = 10
	releaseTrainDefaultCutLockWaitMinutes = 45
)

package agentprofile

func appendDistributionDefaultProviderArgs(profile Profile, model string, launchArgs, args []string) []string {
	if customization.AppendDefaultProviderArgs != nil {
		return customization.AppendDefaultProviderArgs(profile, model, launchArgs, args)
	}
	return args
}

func appendDistributionUpstreamProviderArgs(profile Profile, model string, args []string) []string {
	if customization.AppendUpstreamProviderArgs != nil {
		return customization.AppendUpstreamProviderArgs(profile, model, args)
	}
	return args
}

func launchProcessArgs(agentType string, args []string) []string {
	if customization.LaunchProcessArgs != nil {
		return customization.LaunchProcessArgs(agentType, args)
	}
	return args
}

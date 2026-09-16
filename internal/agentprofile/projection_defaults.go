package agentprofile

func validateDistributionProfile(profile Profile) error {
	return validateProvider(profile.Type, profile.Provider)
}

func providerConfigProjectionFiles(profile Profile) []providerProjection {
	if customization.ProviderProjectionFiles == nil {
		return nil
	}
	var files []providerProjection
	for _, file := range customization.ProviderProjectionFiles(profile) {
		files = append(files, providerProjection{name: file.Path, body: file.Render})
	}
	return files
}
func materializeDistributionConfig(profile Profile, root string) (string, error) {
	if customization.MaterializeConfig != nil {
		return customization.MaterializeConfig(profile, root)
	}
	return "", nil
}
func distributionInstructionsFile(agentType string) string {
	if customization.InstructionsFile != nil {
		return customization.InstructionsFile(agentType)
	}
	return "amux-context.md"
}
func distributionSkillProjectionDir(agentType string) string {
	if customization.SkillProjectionDir != nil {
		return customization.SkillProjectionDir(agentType)
	}
	return ""
}

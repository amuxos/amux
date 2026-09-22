package bridge

import "sort"

func (b *bridgeDB) migrateDistributionDefaults() error {
	replacements := bridgeExtensions.snapshot().DefaultProfileReplacements
	if len(replacements) == 0 {
		return nil
	}
	tx, err := b.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	sources := make([]string, 0, len(replacements))
	for old := range replacements {
		sources = append(sources, old)
	}
	sort.Strings(sources)
	// Only current defaults change. Session identities and other fields are untouched.
	// Registration rejects chained mappings, making these updates order-independent.
	for _, old := range sources {
		for _, statement := range []string{
			`UPDATE feishu_workspaces SET default_agent=? WHERE default_agent=?`,
			`UPDATE feishu_profile_defaults SET default_agent=? WHERE default_agent=?`,
			`UPDATE feishu_bot_profiles SET default_agent_profile=? WHERE default_agent_profile=?`,
		} {
			if _, err := tx.Exec(statement, replacements[old], old); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

package bridge

func (b *bridgeDB) migrateDistributionDefaults() error {
	hook := bridgeExtensions.snapshot().MigrateDefaults
	if hook == nil {
		return nil
	}
	tx, err := b.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := hook(tx); err != nil {
		return err
	}
	return tx.Commit()
}

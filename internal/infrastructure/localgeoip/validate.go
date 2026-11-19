package localgeoip

import (
	"fmt"

	"github.com/oschwald/maxminddb-golang"
)

func validateMetadata(reader *maxminddb.Reader) error {
	meta := reader.Metadata
	if meta.DatabaseType != "DBIP-City-Lite" || meta.IPVersion != 6 || meta.BuildEpoch == 0 {
		return fmt.Errorf("неверные метаданные DB-IP City Lite")
	}
	return nil
}

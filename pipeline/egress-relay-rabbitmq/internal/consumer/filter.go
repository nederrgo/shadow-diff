package consumer

func shouldReportEgress(allow, exchange string) bool {
	if allow == "" {
		return true
	}
	return exchange == allow
}

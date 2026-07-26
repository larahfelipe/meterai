package i18n

import "github.com/larahfelipe/meterai/internal/quota"

var ptBR = &Catalog{
	lang: LangPtBR,
	messages: map[Key]string{
		MenuRefresh:          "Atualizar agora",
		MenuRefreshTooltip:   "Força uma consulta imediata",
		MenuQuit:             "Sair",
		MenuQuitTooltip:      "Encerra o monitor",
		RefreshRejected:      "Consulta recente demais — aguarde um instante",
		MeterResetSuffix:     "· reset em %s",
		BalanceUsedOfLimit:   "%s de %s",
		CountdownNow:         "agora",
		CountdownUnderMinute: "<1min",
		CountdownMinutes:     "%dmin",
		CountdownHours:       "%dh%02d",
		CountdownDays:        "%dd%02dh",
		StatusFirstPoll:      "Consultando…",
		StatusUpdated:        "Atualizado há %s · próxima em %s",
		StatusStale:          "(dados de %s atrás)",
		ErrorUnauthorized:    "Credencial expirada — rode `claude` uma vez para renovar",
		ErrorRateLimited:     "Limite de requisições atingido — aguardando",
		ErrorTransient:       "Sem resposta da API — tentando novamente",
		ErrorProtocol:        "A API mudou de formato — o app precisa ser atualizado",
		ErrorUnrecognized:    "Falha ao consultar",
		ErrorUnexpected:      "Falha inesperada ao consultar",
	},
	meterLabels: map[quota.MeterID]string{
		"anthropic:session":       "Sessão (5h)",
		"anthropic:weekly_all":    "Semanal",
		"anthropic:weekly_opus":   "Semanal (Opus)",
		"anthropic:weekly_sonnet": "Semanal (Sonnet)",
		"anthropic:weekly_cowork": "Semanal (Cowork)",
		"anthropic:spend":         "Créditos",
	},
}

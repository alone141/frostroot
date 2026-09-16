package form

// locales are the UTF-8 locales the form offers. Every one is in Ubuntu's
// locales package, so locale-gen inside the image succeeds.
var locales = []Option{
	{Value: "en_US.UTF-8", Label: "English (United States)"},
	{Value: "en_GB.UTF-8", Label: "English (United Kingdom)"},
	{Value: "C.UTF-8", Label: "C (no language, UTF-8)"},
	{Value: "tr_TR.UTF-8", Label: "Türkçe (Türkiye)"},
	{Value: "de_DE.UTF-8", Label: "Deutsch (Deutschland)"},
	{Value: "fr_FR.UTF-8", Label: "Français (France)"},
	{Value: "es_ES.UTF-8", Label: "Español (España)"},
	{Value: "it_IT.UTF-8", Label: "Italiano (Italia)"},
	{Value: "pt_BR.UTF-8", Label: "Português (Brasil)"},
	{Value: "pt_PT.UTF-8", Label: "Português (Portugal)"},
	{Value: "nl_NL.UTF-8", Label: "Nederlands (Nederland)"},
	{Value: "pl_PL.UTF-8", Label: "Polski (Polska)"},
	{Value: "ru_RU.UTF-8", Label: "Русский (Россия)"},
	{Value: "uk_UA.UTF-8", Label: "Українська (Україна)"},
	{Value: "ar_EG.UTF-8", Label: "العربية (مصر)"},
	{Value: "hi_IN.UTF-8", Label: "हिन्दी (भारत)"},
	{Value: "ja_JP.UTF-8", Label: "日本語 (日本)"},
	{Value: "ko_KR.UTF-8", Label: "한국어 (대한민국)"},
	{Value: "zh_CN.UTF-8", Label: "中文 (中国)"},
	{Value: "zh_TW.UTF-8", Label: "中文 (台灣)"},
}

// Locales returns the offered locales in display order.
func Locales() []Option { return localeOptions() }

// localeOptions returns a copy of the locale list.
func localeOptions() []Option {
	options := make([]Option, len(locales))
	copy(options, locales)
	return options
}

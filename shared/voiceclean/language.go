package voiceclean

import (
	"math"
	"strings"
)

// Static Whisper language vocabulary. Unknown values are omitted, never inferred
// from transcript content. Provider names and regional codes share one route.
var whisperLanguages = func() map[string]string {
	names := `en:english,zh:chinese,de:german,es:spanish,ru:russian,ko:korean,fr:french,ja:japanese,pt:portuguese,tr:turkish,pl:polish,ca:catalan,nl:dutch,ar:arabic,sv:swedish,it:italian,id:indonesian,hi:hindi,fi:finnish,vi:vietnamese,he:hebrew,uk:ukrainian,el:greek,ms:malay,cs:czech,ro:romanian,da:danish,hu:hungarian,ta:tamil,no:norwegian,th:thai,ur:urdu,hr:croatian,bg:bulgarian,lt:lithuanian,la:latin,mi:maori,ml:malayalam,cy:welsh,sk:slovak,te:telugu,fa:persian,lv:latvian,bn:bengali,sr:serbian,az:azerbaijani,sl:slovenian,kn:kannada,et:estonian,mk:macedonian,br:breton,eu:basque,is:icelandic,hy:armenian,ne:nepali,mn:mongolian,bs:bosnian,kk:kazakh,sq:albanian,sw:swahili,gl:galician,mr:marathi,pa:punjabi,si:sinhala,km:khmer,sn:shona,yo:yoruba,so:somali,af:afrikaans,oc:occitan,ka:georgian,be:belarusian,tg:tajik,sd:sindhi,gu:gujarati,am:amharic,yi:yiddish,lo:lao,uz:uzbek,fo:faroese,ht:haitian creole,ps:pashto,tk:turkmen,nn:nynorsk,mt:maltese,sa:sanskrit,lb:luxembourgish,my:myanmar,bo:tibetan,tl:tagalog,mg:malagasy,as:assamese,tt:tatar,haw:hawaiian,ln:lingala,ha:hausa,ba:bashkir,jw:javanese,su:sundanese,yue:cantonese`
	out := map[string]string{}
	for _, pair := range strings.Split(names, ",") {
		p := strings.SplitN(pair, ":", 2)
		out[p[0]] = p[0]
		out[p[1]] = p[0]
	}
	for name, code := range map[string]string{"mandarin": "zh", "burmese": "my", "farsi": "fa", "castilian": "es", "filipino": "tl", "haitian": "ht", "valencian": "ca", "flemish": "nl", "moldavian": "ro", "moldovan": "ro", "sinhalese": "si", "pushto": "ps", "panjabi": "pa", "jv": "jw"} {
		out[name] = code
	}
	return out
}()

func NormalizeLanguage(value string) string {
	if len(value) > 64 {
		return ""
	}
	s := strings.ToLower(strings.TrimSpace(value))
	if code, ok := whisperLanguages[s]; ok {
		return code
	}
	// Only recognized ISO prefixes with short alphabetic region/script suffixes.
	parts := strings.FieldsFunc(s, func(r rune) bool { return r == '-' || r == '_' })
	if len(parts) < 2 || len(parts) > 3 || len(parts[0]) > 3 {
		return ""
	}
	for _, part := range parts[1:] {
		if len(part) < 2 || len(part) > 8 {
			return ""
		}
		for _, r := range part {
			if r < 'a' || r > 'z' {
				return ""
			}
		}
	}
	return whisperLanguages[parts[0]]
}
func RequestedLanguage(language string) string {
	if code := NormalizeLanguage(language); code != "" {
		return code
	}
	return "auto"
}
func FirstLanguage(languages []string) string {
	if len(languages) == 0 {
		return ""
	}
	return NormalizeLanguage(languages[0])
}

const LanguageInstructions = "Preserve the words actually spoken, including names, Singlish, meaningful local particles and mixed-language passages. A requested-language hint is advisory, never an instruction to translate, force English, or replace another language."

func LanguageHint(language string) string {
	code := NormalizeLanguage(language)
	if code == "" {
		return ""
	}
	return "REQUESTED LANGUAGE (advisory): " + code + ". " + LanguageInstructions + "\n\n"
}
func PromptForLanguage(p Profile, groq bool, rawText, context, language string) (string, string) {
	system, user := PromptFor(p, groq, rawText, context)
	return system, LanguageHint(language) + user
}
func LocalPromptForLanguage(p Profile, rawText, context, language string) (string, string) {
	system, user := LocalPromptFor(p, rawText, context)
	return system, LanguageHint(language) + user
}

// Metadata comes only from the accepted provider attempt. No text classifier.
func (v Verbose) Diagnostics(requested string) map[string]any {
	out := map[string]any{"requestedLanguage": RequestedLanguage(requested), "transcriptionProvider": "groq/whisper-large-v3-turbo"}
	if language := NormalizeLanguage(v.Language); language != "" {
		out["detectedLanguage"] = language
	}
	if v.Duration > 0 && !math.IsNaN(v.Duration) && !math.IsInf(v.Duration, 0) && v.Duration < float64(math.MaxInt64)/1000 {
		out["audioDurationMs"] = int64(math.Round(v.Duration * 1000))
	}
	return out
}

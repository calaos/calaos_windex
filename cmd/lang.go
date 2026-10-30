package cmd

import (
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/dustin/go-humanize"
	"golang.org/x/text/language"
)

// MenuItem is one entry of the top menu, shared with calaos.fr: the entries
// and their anchors come from the menus of the www site and differ per language.
type MenuItem struct {
	Name   string
	URL    string
	Button bool
	Icon   bool
}

type ErrorText struct {
	Status  string
	Message string
}

// Locale holds what the listing and the error pages display in one language.
type Locale struct {
	Lang string

	Home          string
	SkipToContent string
	LogoHome      string
	NavMain       string
	NavMobile     string
	NavBurger     string
	NavGithub     string
	Language      string
	Menu          []MenuItem

	IndexOf       string
	Downloads     string
	Breadcrumb    string
	Root          string
	Name          string
	Modified      string
	Size          string
	ParentDir     string
	Folder        string
	File          string
	Empty         string
	EmptyHint     string
	BackToParent  string
	BackDownloads string

	Errors       map[int]ErrorText
	DefaultError ErrorText

	FormatAge  func(time.Time) string
	FormatSize func(uint64) string
}

var localeEn = &Locale{
	Lang:          "en",
	Home:          "https://calaos.fr/en/#top",
	SkipToContent: "Skip to content",
	LogoHome:      "Calaos, home",
	NavMain:       "Main",
	NavMobile:     "Mobile menu",
	NavBurger:     "Menu",
	NavGithub:     "Calaos on GitHub",
	Language:      "Language",
	Menu: []MenuItem{
		{Name: "How it works", URL: "https://calaos.fr/en/#how-it-works"},
		{Name: "Day to day", URL: "https://calaos.fr/en/#day-to-day"},
		{Name: "Interfaces", URL: "https://calaos.fr/en/#interfaces"},
		{Name: "Compatibility", URL: "https://calaos.fr/en/#compatibility"},
		{Name: "Community", URL: "https://calaos.fr/en/#community"},
		{Name: "Documentation", URL: "https://doc.calaos.fr/en/"},
		{Name: "GitHub", URL: "https://github.com/calaos", Icon: true},
		{Name: "Download", URL: "https://calaos.fr/download/", Button: true},
	},
	IndexOf:       "Index of",
	Downloads:     "Downloads",
	Breadcrumb:    "Breadcrumb",
	Root:          "Root",
	Name:          "Name",
	Modified:      "Last modified",
	Size:          "Size",
	ParentDir:     "Parent directory",
	Folder:        "Folder",
	File:          "File",
	Empty:         "This folder is empty",
	EmptyHint:     "There is nothing to download here yet.",
	BackToParent:  "Back to parent folder",
	BackDownloads: "Back to downloads",
	Errors: map[int]ErrorText{
		http.StatusNotFound:            {"Not Found", "The file or folder you are looking for does not exist or is not available."},
		http.StatusForbidden:           {"Forbidden", "You are not allowed to access this resource."},
		http.StatusInternalServerError: {"Internal Server Error", "Something went wrong on our side. Please try again later."},
	},
	DefaultError: ErrorText{"Error", "The request could not be completed."},
	FormatAge:    humanize.Time,
	FormatSize:   humanize.Bytes,
}

var localeFr = &Locale{
	Lang:          "fr",
	Home:          "https://calaos.fr/fr/#top",
	SkipToContent: "Aller au contenu",
	LogoHome:      "Calaos, accueil",
	NavMain:       "Principal",
	NavMobile:     "Menu mobile",
	NavBurger:     "Menu",
	NavGithub:     "Calaos sur GitHub",
	Language:      "Langue",
	Menu: []MenuItem{
		{Name: "Comment ça marche", URL: "https://calaos.fr/fr/#comment"},
		{Name: "Au quotidien", URL: "https://calaos.fr/fr/#quotidien"},
		{Name: "Interfaces", URL: "https://calaos.fr/fr/#interfaces"},
		{Name: "Compatibilité", URL: "https://calaos.fr/fr/#compat"},
		{Name: "Communauté", URL: "https://calaos.fr/fr/#communaute"},
		{Name: "Documentation", URL: "https://doc.calaos.fr"},
		{Name: "GitHub", URL: "https://github.com/calaos", Icon: true},
		{Name: "Télécharger", URL: "https://calaos.fr/download/", Button: true},
	},
	IndexOf:       "Index de",
	Downloads:     "Téléchargements",
	Breadcrumb:    "Fil d’Ariane",
	Root:          "Racine",
	Name:          "Nom",
	Modified:      "Modifié",
	Size:          "Taille",
	ParentDir:     "Dossier parent",
	Folder:        "Dossier",
	File:          "Fichier",
	Empty:         "Ce dossier est vide",
	EmptyHint:     "Il n’y a encore rien à télécharger ici.",
	BackToParent:  "Retour au dossier parent",
	BackDownloads: "Retour aux téléchargements",
	Errors: map[int]ErrorText{
		http.StatusNotFound:            {"Introuvable", "Le fichier ou le dossier demandé n’existe pas ou n’est pas disponible."},
		http.StatusForbidden:           {"Accès refusé", "Vous n’êtes pas autorisé à accéder à cette ressource."},
		http.StatusInternalServerError: {"Erreur interne", "Une erreur est survenue de notre côté. Réessayez plus tard."},
	},
	DefaultError: ErrorText{"Erreur", "La demande n’a pas pu aboutir."},
	FormatAge:    ageFr,
	FormatSize:   sizeFr,
}

// go-humanize only speaks English: same thresholds as its default table.
var ageMagnitudesFr = []humanize.RelTimeMagnitude{
	{D: time.Second, Format: "à l’instant", DivBy: time.Second},
	{D: 2 * time.Second, Format: "%s 1 seconde", DivBy: 1},
	{D: time.Minute, Format: "%s %d secondes", DivBy: time.Second},
	{D: 2 * time.Minute, Format: "%s 1 minute", DivBy: 1},
	{D: time.Hour, Format: "%s %d minutes", DivBy: time.Minute},
	{D: 2 * time.Hour, Format: "%s 1 heure", DivBy: 1},
	{D: humanize.Day, Format: "%s %d heures", DivBy: time.Hour},
	{D: 2 * humanize.Day, Format: "%s 1 jour", DivBy: 1},
	{D: humanize.Week, Format: "%s %d jours", DivBy: humanize.Day},
	{D: 2 * humanize.Week, Format: "%s 1 semaine", DivBy: 1},
	{D: humanize.Month, Format: "%s %d semaines", DivBy: humanize.Week},
	{D: 2 * humanize.Month, Format: "%s 1 mois", DivBy: 1},
	{D: humanize.Year, Format: "%s %d mois", DivBy: humanize.Month},
	{D: 18 * humanize.Month, Format: "%s 1 an", DivBy: 1},
	{D: 2 * humanize.Year, Format: "%s 2 ans", DivBy: 1},
	{D: humanize.LongTime, Format: "%s %d ans", DivBy: humanize.Year},
	{D: math.MaxInt64, Format: "%s très longtemps", DivBy: 1},
}

func ageFr(then time.Time) string {
	return humanize.CustomRelTime(then, time.Now(), "il y a", "dans", ageMagnitudesFr)
}

var sizeReplacerFr = strings.NewReplacer(".", ",", "kB", "ko", "MB", "Mo", "GB", "Go", "TB", "To", "PB", "Po", "EB", "Eo", "B", "o")

func sizeFr(n uint64) string {
	return sizeReplacerFr.Replace(humanize.Bytes(n))
}

// English comes first: the matcher returns it when nothing else fits.
var langMatcher = language.NewMatcher([]language.Tag{language.English, language.French})

func negotiateLocale(req *http.Request) *Locale {
	_, i := language.MatchStrings(langMatcher, req.Header.Get("Accept-Language"))
	if i == 1 {
		return localeFr
	}
	return localeEn
}

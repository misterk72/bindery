package indexer

import (
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

func TestSearchTitle(t *testing.T) {
	cases := []struct {
		name    string
		book    models.Book
		allowed []string
		want    string
	}{
		{
			name: "bilingual title with a non English book language uses the localized half",
			book: models.Book{Title: "El imperio final / The Final Empire", Language: "spa"},
			want: "El imperio final",
		},
		{
			name: "two letter and regional language codes count",
			book: models.Book{Title: "Der Name des Windes / The Name of the Wind", Language: "de-DE"},
			want: "Der Name des Windes",
		},
		{
			name:    "no book language: a non English profile is the evidence",
			book:    models.Book{Title: "El imperio final / The Final Empire"},
			allowed: []string{"es"},
			want:    "El imperio final",
		},
		{
			name: "OriginalTitle on the right confirms the pair even for an English book",
			book: models.Book{Title: "The Final Empire / Mistborn", OriginalTitle: "Mistborn", Language: "eng"},
			want: "The Final Empire",
		},
		{
			name: "OriginalTitle on the left reverses the pair",
			book: models.Book{Title: "The Final Empire / El imperio final", OriginalTitle: "the final empire", Language: "spa"},
			want: "El imperio final",
		},
		{
			name: "the returned half keeps its own qualifier and spelling",
			book: models.Book{Title: "  El imperio final (Nacidos de la bruma 1) / The Final Empire", Language: "spa"},
			want: "El imperio final (Nacidos de la bruma 1)",
		},
		{
			name: "an English bundle stays whole",
			book: models.Book{Title: "Summer Stars: Second Nature / One Summer", Language: "eng"},
			want: "Summer Stars: Second Nature / One Summer",
		},
		{
			name:    "no language anywhere: an English or any profile is no evidence",
			book:    models.Book{Title: "Second Nature / One Summer"},
			allowed: []string{"eng", "any"},
			want:    "Second Nature / One Summer",
		},
		{
			name:    "an English book is not split by a non English profile",
			book:    models.Book{Title: "Second Nature / One Summer", Language: "en"},
			allowed: []string{"ger"},
			want:    "Second Nature / One Summer",
		},
		{
			name: "three parts are a bundle",
			book: models.Book{Title: "Uno / Dos / Tres", Language: "spa"},
			want: "Uno / Dos / Tres",
		},
		{
			name: "a slash without spaces is part of the title",
			book: models.Book{Title: "Fahrenheit 9/11", Language: "spa"},
			want: "Fahrenheit 9/11",
		},
		{
			name: "a slash inside parentheses is a qualifier",
			book: models.Book{Title: "Dune (Libro 1 / Parte 2)", Language: "spa"},
			want: "Dune (Libro 1 / Parte 2)",
		},
		{
			name: "a slash inside brackets is a qualifier",
			book: models.Book{Title: "Dune [Tapa dura / Ilustrado]", Language: "spa"},
			want: "Dune [Tapa dura / Ilustrado]",
		},
		{
			name: "a half with no letters or digits is not a title",
			book: models.Book{Title: "El imperio final / ...", Language: "spa"},
			want: "El imperio final / ...",
		},
		{
			name: "non Latin scripts split the same way",
			book: models.Book{Title: "ノルウェイの森 / Norwegian Wood", Language: "jpn"},
			want: "ノルウェイの森",
		},
		{
			name: "a plain title is untouched",
			book: models.Book{Title: "Hinter verzauberten Fenstern", Language: "ger"},
			want: "Hinter verzauberten Fenstern",
		},
		{
			name: "an empty title stays empty",
			book: models.Book{Language: "spa"},
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SearchTitle(tc.book, tc.allowed); got != tc.want {
				t.Errorf("SearchTitle(%q) = %q, want %q", tc.book.Title, got, tc.want)
			}
		})
	}
}

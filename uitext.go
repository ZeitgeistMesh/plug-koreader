package main

// uitext.go — every sentence a person reads on the Settings and Books pages, and the messages
// the book-server code raises. Written by Opus, one place, so a wording change touches nothing
// else. {name} markers are filled by fill.

type uiText struct {
	NavSettings, SettingsTitle, BooksTitle                                     string
	LabelURL, LabelUser, LabelPassword                                         string
	PlaceholderURL, HintURL, HintUser, PlaceholderPasswordSet, HintPassword    string
	BtnTest, BtnSave, BtnDisconnect                                            string
	StatusNone, StatusOK, StatusSavedOK, StatusSavedFailed, StatusDisconnected string

	SearchPlaceholder, EmptyNotConnected, EmptyLoading, EmptyNoneMatch, EmptyNoneAtAll string
	UpdatedLine, ReadingLine, FailedLine, TruncatedLine                                string
	ColTitle, ColAuthor, ColFormats, DownloadTitle                                     string

	ErrNoURL, ErrHTTPS, ErrNotWeb, ErrTooLong, ErrNoKey, ErrStore, ErrSave, ErrConnectFirst string
	ErrAgentOnlyYou, ErrDemo, ErrAuth, ErrUnreachable, ErrAddressBlocked, ErrRedirectOff    string
	ErrRedirects, ErrStatus, ErrNotCatalog, ErrNoCatalogFound, ErrTooLarge, ErrFileBig      string
	ErrFetchBook                                                                            string
}

var ui = uiText{
	NavSettings:   "Settings",
	SettingsTitle: "Book server",
	BooksTitle:    "Books",

	LabelURL:               "Address",
	LabelUser:              "Username",
	LabelPassword:          "Password",
	PlaceholderURL:         "https://books.example.com",
	HintURL:                "Your server's home page or its catalog (OPDS) address. It must start with https://.",
	HintUser:               "Leave blank if your server has no login.",
	PlaceholderPasswordSet: "Saved. Leave blank to keep it.",
	HintPassword:           "Stored encrypted. It is never shown again.",

	BtnTest:       "Test",
	BtnSave:       "Save",
	BtnDisconnect: "Disconnect",

	StatusNone:         "No book server connected.",
	StatusOK:           "Connected to {server}.",
	StatusSavedOK:      "Saved. Connected to {server}.",
	StatusSavedFailed:  "Saved, but the test failed. {problem}",
	StatusDisconnected: "Disconnected.",

	SearchPlaceholder: "Search title or author",
	EmptyNotConnected: "Connect your book server in",
	EmptyLoading:      "Getting your books from your server…",
	EmptyNoneMatch:    "No book matches your search.",
	EmptyNoneAtAll:    "Your server has no books.",
	UpdatedLine:       "Updated {when}",
	ReadingLine:       "Updating…",
	FailedLine:        "Could not update, so this is the last list. {problem}",
	TruncatedLine:     "Showing the first {n} books.",
	ColTitle:          "Title",
	ColAuthor:         "Author",
	ColFormats:        "Formats",
	DownloadTitle:     "Download",

	ErrNoURL:          "Enter your book server's address, like https://books.example.com.",
	ErrHTTPS:          "The address must start with https:// to keep your password safe.",
	ErrNotWeb:         "Enter a full web address, like https://books.example.com.",
	ErrTooLong:        "The username or password is too long to save.",
	ErrNoKey:          "Passwords cannot be saved yet because encryption is not set up here.",
	ErrStore:          "Could not save the password, so try again in a moment.",
	ErrSave:           "Could not save your settings, so try again in a moment.",
	ErrConnectFirst:   "Connect your book server in Settings first.",
	ErrAgentOnlyYou:   "Only you can connect a book server, from your own signed-in page.",
	ErrDemo:           "The demo cannot connect a book server, so sign in to use your own.",
	ErrAuth:           "The server did not accept that username or password, so check them and try again.",
	ErrUnreachable:    "Could not reach your server ({why}), so check the address and try again.",
	ErrAddressBlocked: "That address cannot be used here, so try your server's public address.",
	ErrRedirectOff:    "The server sent us to a different address, so enter that address instead.",
	ErrRedirects:      "The server kept sending us to other addresses, so check the address and try again.",
	ErrStatus:         "The server answered with error {code}, so check the address and try again.",
	ErrNotCatalog:     "That address does not show a list of books, so try your server's catalog address.",
	ErrNoCatalogFound: "No book catalog was found there, so enter the catalog address, like https://books.example.com/opds.",
	ErrTooLarge:       "The server's catalog page is too large to read.",
	ErrFileBig:        "That book file is too large to download here.",
	ErrFetchBook:      "Could not get that book from your server ({why}), so try again later.",
}


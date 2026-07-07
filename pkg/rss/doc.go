// Package rss generates a fully compliant RSS 2.0 feed with iTunes and
// Podcasting 2.0 (https://podcastindex.org/namespace/1.0) extensions.
//
// This package is a fork of github.com/eduncan911/podcast v1.4.2 (MIT
// licensed, see LICENSE in this directory). It was brought in-tree to add
// support for the podcast: XML namespace (transcripts, chapters, guid,
// medium, locked, person, socialInteract), which the upstream library's
// closed structs cannot express.
//
// The upstream API and its output format are preserved: feeds generated
// with only the upstream feature set are byte-identical except for the
// added xmlns:podcast declaration on the <rss> element.
package rss

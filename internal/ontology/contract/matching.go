package contract

import (
	"slices"
	"strings"
)

// Pinned PostgreSQL English list: https://raw.githubusercontent.com/postgres/postgres/3f1197191685512d1968ffd8958497159944c2a1/src/backend/snowball/stopwords/english.stop
const vocabularyStopWords = `i
me
my
myself
we
our
ours
ourselves
you
your
yours
yourself
yourselves
he
him
his
himself
she
her
hers
herself
it
its
itself
they
them
their
theirs
themselves
what
which
who
whom
this
that
these
those
am
is
are
was
were
be
been
being
have
has
had
having
do
does
did
doing
a
an
the
and
but
if
or
because
as
until
while
of
at
by
for
with
about
against
between
into
through
during
before
after
above
below
to
from
up
down
in
out
on
off
over
under
again
further
then
once
here
there
when
where
why
how
all
any
both
each
few
more
most
other
some
such
no
nor
not
only
own
same
so
than
too
very
s
t
can
will
just
don
should
now
`

func VocabularyStopWords() []string {
	return strings.Fields(vocabularyStopWords)
}

func LooseVocabularyQuery(words []string) string {
	stopWords := VocabularyStopWords()
	filtered := make([]string, 0, len(words))
	for _, word := range words {
		if !slices.Contains(stopWords, word) {
			filtered = append(filtered, word)
		}
	}
	return strings.Join(filtered, " OR ")
}

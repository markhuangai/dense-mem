package recallservice

import (
	"strconv"
	"strings"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
)

type currentnessTemporalFrame struct {
	hasContentDate       bool
	newestContentDate    time.Time
	useFragmentTimestamp bool
	newestFragmentTime   time.Time
}

func currentnessTemporalAdjustment(query string, entry rrfEntry, frame currentnessTemporalFrame) float64 {
	if !frame.hasContentDate && !frame.useFragmentTimestamp {
		return 0
	}
	queryText := rerankText(query)
	contentText := rerankText(entry.Content)
	if queryText == "" || contentText == "" || !rerankMatchesQueryIdentifiers(queryText, contentText) {
		return 0
	}

	if frame.hasContentDate {
		contentDate := latestCurrentnessTemporalDateInEntry(entry)
		if contentDate.IsZero() {
			return -0.006
		}
		return contentDateTemporalDelta(frame.newestContentDate.Sub(contentDate))
	}

	fragmentTime := latestFragmentTimestamp(entry.CreatedAt, entry.UpdatedAt)
	if fragmentTime.IsZero() {
		return 0
	}
	return fragmentTimestampTemporalDelta(frame.newestFragmentTime.Sub(fragmentTime))
}

func contentDateTemporalDelta(age time.Duration) float64 {
	if age < 0 {
		age = 0
	}
	switch {
	case age == 0:
		return 0.028
	case age <= 72*time.Hour:
		return 0.014
	case age >= 7*24*time.Hour:
		return -0.026
	case age >= 24*time.Hour:
		return -0.018
	default:
		return 0
	}
}

func fragmentTimestampTemporalDelta(age time.Duration) float64 {
	if age < 0 {
		age = 0
	}
	switch {
	case age == 0:
		return 0.014
	case age <= 72*time.Hour:
		return 0.007
	case age >= 7*24*time.Hour:
		return -0.014
	case age >= 24*time.Hour:
		return -0.010
	default:
		return 0
	}
}

func expiredValidityAdjustment(query string, entry rrfEntry, frame currentnessTemporalFrame) float64 {
	queryText := rerankText(query)
	contentText := rerankText(entry.Content)
	if queryText == "" || contentText == "" || !rerankMatchesQueryIdentifiers(queryText, contentText) {
		return 0
	}
	validityEnd := latestValidityEndDateInEntry(entry)
	if validityEnd.IsZero() {
		return 0
	}
	asOf := currentnessAsOfTime(query, entry, frame)
	if asOf.IsZero() || !utcDate(asOf).After(utcDate(validityEnd)) {
		return 0
	}
	return -0.04
}

func currentnessAsOfTime(query string, entry rrfEntry, frame currentnessTemporalFrame) time.Time {
	if queryDate := latestTemporalDateInText(query, latestFragmentTimestamp(entry.CreatedAt, entry.UpdatedAt)); !queryDate.IsZero() {
		return queryDate
	}
	if !frame.newestContentDate.IsZero() {
		return frame.newestContentDate
	}
	if !frame.newestFragmentTime.IsZero() {
		return frame.newestFragmentTime
	}
	return latestFragmentTimestamp(entry.CreatedAt, entry.UpdatedAt)
}

func currentnessTemporalFrameFor(query string, entries []rrfEntry) currentnessTemporalFrame {
	queryText := rerankText(query)
	var frame currentnessTemporalFrame
	var oldestFragmentTime time.Time
	for _, entry := range entries {
		contentText := rerankText(entry.Content)
		if queryText == "" || contentText == "" || !rerankMatchesQueryIdentifiers(queryText, contentText) {
			continue
		}
		if contentDate := latestCurrentnessTemporalDateInEntry(entry); !contentDate.IsZero() {
			frame.hasContentDate = true
			if frame.newestContentDate.IsZero() || contentDate.After(frame.newestContentDate) {
				frame.newestContentDate = contentDate
			}
		}
		fragmentTime := latestFragmentTimestamp(entry.CreatedAt, entry.UpdatedAt)
		if fragmentTime.IsZero() {
			continue
		}
		if oldestFragmentTime.IsZero() || fragmentTime.Before(oldestFragmentTime) {
			oldestFragmentTime = fragmentTime
		}
		if frame.newestFragmentTime.IsZero() || fragmentTime.After(frame.newestFragmentTime) {
			frame.newestFragmentTime = fragmentTime
		}
	}
	if !frame.hasContentDate && !oldestFragmentTime.IsZero() && frame.newestFragmentTime.Sub(oldestFragmentTime) >= 24*time.Hour {
		frame.useFragmentTimestamp = true
	}
	return frame
}

func latestFragmentTimestamp(createdAt, updatedAt time.Time) time.Time {
	latest := createdAt
	if updatedAt.After(latest) {
		latest = updatedAt
	}
	if latest.IsZero() {
		return time.Time{}
	}
	return latest.UTC()
}

func temporalRankTimeForRecall(query string, validFrom *time.Time, recordedAt time.Time, contentParts ...string) time.Time {
	return temporalRankTimeForRecallWithEvidence(query, validFrom, recordedAt, nil, nil, contentParts...)
}

func temporalRankTimeForRecallWithEvidence(query string, validFrom *time.Time, recordedAt time.Time, evidence []domain.Evidence, evidenceFragments map[string]*domain.Fragment, contentParts ...string) time.Time {
	if !isCurrentnessQuery(query) {
		return time.Time{}
	}
	if validFrom != nil && !validFrom.IsZero() {
		return validFrom.UTC()
	}
	if contentDate := latestTemporalDateInText(strings.Join(contentParts, " "), recordedAt); !contentDate.IsZero() {
		return contentDate
	}
	if evidenceDate := latestTemporalDateInEvidence(query, evidence, evidenceFragments); !evidenceDate.IsZero() {
		return evidenceDate
	}
	if !recordedAt.IsZero() {
		return recordedAt.UTC()
	}
	return time.Time{}
}

type typedCurrentnessTemporalFrame struct {
	asOf time.Time
}

func typedCurrentnessTemporalFrameForFacts(query string, facts []hydratedFactRecallCandidate, evidenceFragments map[string]*domain.Fragment) typedCurrentnessTemporalFrame {
	var frame typedCurrentnessTemporalFrame
	if !isCurrentnessQuery(query) {
		return frame
	}
	for _, candidate := range facts {
		f := candidate.Fact
		if f == nil || !factMatchesQueryIdentifiers(query, f) {
			continue
		}
		updateTypedCurrentnessTemporalFrame(&frame, query, f.ValidFrom, f.RecordedAt, f.Evidence, evidenceFragments, f.Subject, f.Predicate, f.Object)
	}
	return frame
}

func typedCurrentnessTemporalFrameForClaims(query string, claims []*domain.Claim, evidenceFragments map[string]*domain.Fragment) typedCurrentnessTemporalFrame {
	var frame typedCurrentnessTemporalFrame
	if !isCurrentnessQuery(query) {
		return frame
	}
	for _, c := range claims {
		if c == nil || !claimMatchesQueryIdentifiers(query, c) {
			continue
		}
		updateTypedCurrentnessTemporalFrame(&frame, query, c.ValidFrom, c.RecordedAt, c.Evidence, evidenceFragments, c.Subject, c.Predicate, c.Object)
	}
	return frame
}

func updateTypedCurrentnessTemporalFrame(frame *typedCurrentnessTemporalFrame, query string, validFrom *time.Time, recordedAt time.Time, evidence []domain.Evidence, evidenceFragments map[string]*domain.Fragment, contentParts ...string) {
	if frame == nil {
		return
	}
	candidateAsOf := latestTypedCurrentnessAsOfSignal(query, validFrom, recordedAt, evidence, evidenceFragments, contentParts...)
	if candidateAsOf.IsZero() {
		return
	}
	if frame.asOf.IsZero() || candidateAsOf.After(frame.asOf) {
		frame.asOf = candidateAsOf
	}
}

func latestTypedCurrentnessAsOfSignal(query string, validFrom *time.Time, recordedAt time.Time, evidence []domain.Evidence, evidenceFragments map[string]*domain.Fragment, contentParts ...string) time.Time {
	var latest time.Time
	if validFrom != nil && !validFrom.IsZero() {
		latest = validFrom.UTC()
	}
	if contentDate := latestTemporalDateInText(strings.Join(contentParts, " "), recordedAt); !contentDate.IsZero() && (latest.IsZero() || contentDate.After(latest)) {
		latest = contentDate
	}
	if evidenceDate := latestTemporalDateInEvidence(query, evidence, evidenceFragments); !evidenceDate.IsZero() && (latest.IsZero() || evidenceDate.After(latest)) {
		latest = evidenceDate
	}
	if !recordedAt.IsZero() {
		recordedAt = recordedAt.UTC()
		if latest.IsZero() || recordedAt.After(latest) {
			latest = recordedAt
		}
	}
	return latest
}

func typedTemporalRankTimeForRecallWithEvidence(query string, validFrom, validTo *time.Time, recordedAt time.Time, evidence []domain.Evidence, evidenceFragments map[string]*domain.Fragment, frame typedCurrentnessTemporalFrame, contentParts ...string) time.Time {
	rank := temporalRankTimeForRecallWithEvidence(query, validFrom, recordedAt, evidence, evidenceFragments, contentParts...)
	if rank.IsZero() || !typedValidityExpiredForRecall(query, validTo, recordedAt, frame) {
		return rank
	}
	return time.Time{}
}

func typedValidityExpiredForRecall(query string, validTo *time.Time, recordedAt time.Time, frame typedCurrentnessTemporalFrame) bool {
	if validTo == nil || validTo.IsZero() || !isCurrentnessQuery(query) {
		return false
	}
	asOf := typedCurrentnessAsOfTime(query, recordedAt, frame)
	if asOf.IsZero() {
		return false
	}
	return !validTo.UTC().After(asOf.UTC())
}

func typedCurrentnessAsOfTime(query string, recordedAt time.Time, frame typedCurrentnessTemporalFrame) time.Time {
	if queryDate := latestTemporalDateInText(query, recordedAt); !queryDate.IsZero() {
		return queryDate.UTC()
	}
	if !frame.asOf.IsZero() {
		return frame.asOf.UTC()
	}
	if !recordedAt.IsZero() {
		return recordedAt.UTC()
	}
	return time.Time{}
}

func factMatchesQueryIdentifiers(query string, fact *domain.Fact) bool {
	if fact == nil {
		return false
	}
	return knowledgeTripleMatchesQueryIdentifiers(query, fact.Subject, fact.Predicate, fact.Object)
}

func claimMatchesQueryIdentifiers(query string, claim *domain.Claim) bool {
	if claim == nil {
		return false
	}
	return knowledgeTripleMatchesQueryIdentifiers(query, claim.Subject, claim.Predicate, claim.Object)
}

func knowledgeTripleMatchesQueryIdentifiers(query string, parts ...string) bool {
	queryText := rerankText(query)
	if len(rerankIdentifiers(queryText)) == 0 {
		return true
	}
	return rerankMatchesQueryIdentifiers(queryText, rerankText(strings.Join(parts, " ")))
}
func latestISODateInText(value string) time.Time {
	var latest time.Time
	for _, field := range strings.Fields(value) {
		token := strings.Trim(field, ".,:;!?()[]{}\"'")
		if len(token) != len("2006-01-02") {
			continue
		}
		parsed, err := time.Parse("2006-01-02", token)
		if err != nil {
			continue
		}
		parsed = parsed.UTC()
		if latest.IsZero() || parsed.After(latest) {
			latest = parsed
		}
	}
	return latest
}

func latestNumericDateInText(value string, anchor time.Time) time.Time {
	anchorYear := 0
	if !anchor.IsZero() {
		anchorYear = anchor.UTC().Year()
	}
	latest := time.Time{}
	for _, field := range strings.Fields(value) {
		token := strings.Trim(strings.ToLower(field), ".,:;!?()[]{}\"'")
		candidate, ok := numericDateToken(token, anchorYear)
		if !ok {
			continue
		}
		if latest.IsZero() || candidate.After(latest) {
			latest = candidate
		}
	}
	return latest
}

func numericDateToken(token string, anchorYear int) (time.Time, bool) {
	parts := strings.Split(token, "/")
	if len(parts) != 2 && len(parts) != 3 {
		return time.Time{}, false
	}
	nums := make([]int, len(parts))
	for i, part := range parts {
		if part == "" {
			return time.Time{}, false
		}
		value, err := strconv.Atoi(part)
		if err != nil {
			return time.Time{}, false
		}
		nums[i] = value
	}
	if len(parts) == 3 {
		if year, ok := parseYear(parts[0]); ok {
			return calendarDate(year, time.Month(nums[1]), nums[2])
		}
		year, ok := parseYear(parts[2])
		if !ok {
			return time.Time{}, false
		}
		month, day, ok := unambiguousNumericMonthDay(nums[0], nums[1])
		if !ok {
			return time.Time{}, false
		}
		return calendarDate(year, time.Month(month), day)
	}
	if anchorYear == 0 {
		return time.Time{}, false
	}
	month, day, ok := unambiguousNumericMonthDay(nums[0], nums[1])
	if !ok {
		return time.Time{}, false
	}
	return calendarDate(anchorYear, time.Month(month), day)
}

func unambiguousNumericMonthDay(first, second int) (int, int, bool) {
	switch {
	case first >= 1 && first <= 12 && second > 12 && second <= 31:
		return first, second, true
	case first > 12 && first <= 31 && second >= 1 && second <= 12:
		return second, first, true
	default:
		return 0, 0, false
	}
}

func latestTemporalDateInEntry(entry rrfEntry) time.Time {
	return latestTemporalDateInText(entry.Content, latestFragmentTimestamp(entry.CreatedAt, entry.UpdatedAt))
}

func latestCurrentnessTemporalDateInEntry(entry rrfEntry) time.Time {
	latest := latestTemporalDateInEntry(entry)
	if latest.IsZero() {
		return time.Time{}
	}
	validityEnd := latestValidityEndDateInEntry(entry)
	if !validityEnd.IsZero() && utcDate(latest).Equal(utcDate(validityEnd)) {
		return time.Time{}
	}
	return latest
}

func latestValidityEndDateInEntry(entry rrfEntry) time.Time {
	return latestValidityEndDateInText(entry.Content, latestFragmentTimestamp(entry.CreatedAt, entry.UpdatedAt))
}

func latestTemporalDateInEvidence(query string, evidence []domain.Evidence, evidenceFragments map[string]*domain.Fragment) time.Time {
	if len(evidence) == 0 || len(evidenceFragments) == 0 {
		return time.Time{}
	}
	queryText := rerankText(query)
	latest := time.Time{}
	for _, item := range evidence {
		if item.FragmentID == "" {
			continue
		}
		fragment := evidenceFragments[item.FragmentID]
		if fragment == nil {
			continue
		}
		contentText := rerankText(fragment.Content)
		if queryText == "" || contentText == "" || !rerankMatchesQueryIdentifiers(queryText, contentText) {
			continue
		}
		candidate := latestTemporalDateInEntry(rrfEntry{
			Content:   fragment.Content,
			CreatedAt: fragment.CreatedAt,
			UpdatedAt: fragment.UpdatedAt,
		})
		if !candidate.IsZero() && (latest.IsZero() || candidate.After(latest)) {
			latest = candidate
		}
	}
	return latest
}

func latestTemporalDateInText(value string, anchor time.Time) time.Time {
	latest := latestISODateInText(value)
	numeric := latestNumericDateInText(value, anchor)
	if !numeric.IsZero() && (latest.IsZero() || numeric.After(latest)) {
		latest = numeric
	}
	monthName := latestMonthNameDateInText(value, anchor)
	if !monthName.IsZero() && (latest.IsZero() || monthName.After(latest)) {
		latest = monthName
	}
	relative := latestRelativeDateInText(value, anchor)
	if !relative.IsZero() && (latest.IsZero() || relative.After(latest)) {
		latest = relative
	}
	weekday := latestWeekdayDateInText(value, anchor)
	if !weekday.IsZero() && (latest.IsZero() || weekday.After(latest)) {
		latest = weekday
	}
	return latest
}

func latestValidityEndDateInText(value string, anchor time.Time) time.Time {
	text := rerankText(value)
	if text == "" {
		return time.Time{}
	}
	fields := strings.Fields(text)
	latest := time.Time{}
	for i := range fields {
		if !validityEndCueAt(fields, i) {
			continue
		}
		for j := i + 1; j < len(fields) && j <= i+4; j++ {
			candidate, ok := temporalDateAtFields(fields, j, anchor)
			if !ok {
				continue
			}
			if latest.IsZero() || candidate.After(latest) {
				latest = candidate
			}
			break
		}
	}
	return latest
}

func validityEndCueAt(fields []string, index int) bool {
	if index < 0 || index >= len(fields) {
		return false
	}
	field := fields[index]
	if field == "valid" && index+1 < len(fields) && validityEndPreposition(fields[index+1]) {
		return true
	}
	if validityEndPreposition(field) && index > 0 && fields[index-1] == "valid" {
		return true
	}
	switch field {
	case "expires", "expire", "expired", "expiration", "ends", "ended", "ending", "sunsets", "sunset", "retired":
		return true
	default:
		return false
	}
}

func validityEndPreposition(token string) bool {
	switch token {
	case "through", "until", "to", "before":
		return true
	default:
		return false
	}
}

func temporalDateAtFields(fields []string, index int, anchor time.Time) (time.Time, bool) {
	if index < 0 || index >= len(fields) {
		return time.Time{}, false
	}
	if parsed, err := time.Parse("2006-01-02", fields[index]); err == nil {
		return parsed.UTC(), true
	}
	anchorYear := 0
	if !anchor.IsZero() {
		anchorYear = anchor.UTC().Year()
	}
	if parsed, ok := numericDateToken(fields[index], anchorYear); ok {
		return parsed, true
	}
	if month, ok := monthNameNumber(fields[index]); ok {
		if index+1 < len(fields) {
			if day, ok := parseDayOfMonth(fields[index+1]); ok {
				year := anchorYear
				if index+2 < len(fields) {
					if parsedYear, ok := parseYear(fields[index+2]); ok {
						year = parsedYear
					}
				}
				return calendarDate(year, month, day)
			}
		}
	}
	if day, ok := parseDayOfMonth(fields[index]); ok && index+1 < len(fields) {
		if month, ok := monthNameNumber(fields[index+1]); ok {
			year := anchorYear
			if index+2 < len(fields) {
				if parsedYear, ok := parseYear(fields[index+2]); ok {
					year = parsedYear
				}
			}
			return calendarDate(year, month, day)
		}
	}
	return time.Time{}, false
}

func latestMonthNameDateInText(value string, anchor time.Time) time.Time {
	text := rerankText(value)
	if text == "" {
		return time.Time{}
	}
	anchorYear := 0
	if !anchor.IsZero() {
		anchorYear = anchor.UTC().Year()
	}
	latest := time.Time{}
	fields := strings.Fields(text)
	for i, field := range fields {
		month, ok := monthNameNumber(field)
		if !ok {
			continue
		}
		if i+1 < len(fields) {
			if day, ok := parseDayOfMonth(fields[i+1]); ok {
				year := anchorYear
				if i+2 < len(fields) {
					if parsedYear, ok := parseYear(fields[i+2]); ok {
						year = parsedYear
					}
				}
				if candidate, ok := calendarDate(year, month, day); ok && (latest.IsZero() || candidate.After(latest)) {
					latest = candidate
				}
			}
		}
		if i > 0 {
			if day, ok := parseDayOfMonth(fields[i-1]); ok {
				year := anchorYear
				if i+1 < len(fields) {
					if parsedYear, ok := parseYear(fields[i+1]); ok {
						year = parsedYear
					}
				}
				if candidate, ok := calendarDate(year, month, day); ok && (latest.IsZero() || candidate.After(latest)) {
					latest = candidate
				}
			}
		}
	}
	return latest
}

func latestRelativeDateInText(value string, anchor time.Time) time.Time {
	if anchor.IsZero() {
		return time.Time{}
	}
	text := rerankText(value)
	if text == "" {
		return time.Time{}
	}
	anchorDate := utcDate(anchor)
	latest := time.Time{}
	add := func(candidate time.Time) {
		if !candidate.IsZero() && (latest.IsZero() || candidate.After(latest)) {
			latest = candidate
		}
	}
	fields := strings.Fields(text)
	for i, field := range fields {
		switch field {
		case "today":
			add(anchorDate)
		case "yesterday":
			add(anchorDate.AddDate(0, 0, -1))
		case "ago":
			if i >= 2 && (fields[i-1] == "day" || fields[i-1] == "days") {
				if days, ok := relativeDayCount(fields[i-2]); ok {
					add(anchorDate.AddDate(0, 0, -days))
				}
			}
		case "week":
			if i >= 1 && fields[i-1] == "last" {
				add(anchorDate.AddDate(0, 0, -7))
			}
		}
	}
	return latest
}

func latestWeekdayDateInText(value string, anchor time.Time) time.Time {
	if anchor.IsZero() {
		return time.Time{}
	}
	text := rerankText(value)
	if text == "" {
		return time.Time{}
	}
	anchorDate := utcDate(anchor)
	latest := time.Time{}
	fields := strings.Fields(text)
	for i, field := range fields {
		weekday, ok := weekdayNumber(field)
		if !ok {
			continue
		}
		if i > 0 && fields[i-1] == "next" {
			continue
		}
		daysBack := (int(anchorDate.Weekday()) - int(weekday) + 7) % 7
		if i > 0 && fields[i-1] == "last" && daysBack == 0 {
			daysBack = 7
		}
		candidate := anchorDate.AddDate(0, 0, -daysBack)
		if latest.IsZero() || candidate.After(latest) {
			latest = candidate
		}
	}
	return latest
}

func monthNameNumber(token string) (time.Month, bool) {
	switch token {
	case "jan", "january":
		return time.January, true
	case "feb", "february":
		return time.February, true
	case "mar", "march":
		return time.March, true
	case "apr", "april":
		return time.April, true
	case "may":
		return time.May, true
	case "jun", "june":
		return time.June, true
	case "jul", "july":
		return time.July, true
	case "aug", "august":
		return time.August, true
	case "sep", "sept", "september":
		return time.September, true
	case "oct", "october":
		return time.October, true
	case "nov", "november":
		return time.November, true
	case "dec", "december":
		return time.December, true
	default:
		return 0, false
	}
}

func weekdayNumber(token string) (time.Weekday, bool) {
	switch token {
	case "sun", "sunday":
		return time.Sunday, true
	case "mon", "monday":
		return time.Monday, true
	case "tue", "tues", "tuesday":
		return time.Tuesday, true
	case "wed", "wednesday":
		return time.Wednesday, true
	case "thu", "thur", "thurs", "thursday":
		return time.Thursday, true
	case "fri", "friday":
		return time.Friday, true
	case "sat", "saturday":
		return time.Saturday, true
	default:
		return 0, false
	}
}

func parseDayOfMonth(token string) (int, bool) {
	day, err := strconv.Atoi(strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(token, "st"), "nd"), "rd"), "th"))
	if err != nil || day < 1 || day > 31 {
		return 0, false
	}
	return day, true
}

func parseYear(token string) (int, bool) {
	year, err := strconv.Atoi(token)
	if err != nil || year < 1900 || year > 3000 {
		return 0, false
	}
	return year, true
}

func calendarDate(year int, month time.Month, day int) (time.Time, bool) {
	if year == 0 || month < time.January || month > time.December || day < 1 || day > daysInMonth(year, month) {
		return time.Time{}, false
	}
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC), true
}

func daysInMonth(year int, month time.Month) int {
	return time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

func utcDate(value time.Time) time.Time {
	value = value.UTC()
	year, month, day := value.Date()
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

func relativeDayCount(token string) (int, bool) {
	switch token {
	case "one", "a", "1":
		return 1, true
	case "two", "2":
		return 2, true
	case "three", "3":
		return 3, true
	case "four", "4":
		return 4, true
	case "five", "5":
		return 5, true
	case "six", "6":
		return 6, true
	case "seven", "7":
		return 7, true
	default:
		return 0, false
	}
}

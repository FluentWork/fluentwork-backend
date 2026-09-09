package topic

import (
	"context"
	"time"
)

// UpdateOnCheckin advances the UTC-day streak. A second checkin on the same
// UTC day does not increment again. A gap of one or more days resets to 1.
func UpdateOnCheckin(ctx context.Context, store Store, userID string, now time.Time) (Streak, error) {
	today := utcDate(now)
	st, err := store.GetStreak(ctx, userID)
	if err != nil {
		return Streak{}, err
	}
	st.UserID = userID
	if st.DeletedAt != nil {
		st.DeletedAt = nil
	}
	if st.LastCheckinDate != nil && utcDate(*st.LastCheckinDate).Equal(today) {
		st.UpdatedAt = now.UTC()
		if err := store.UpsertStreak(ctx, st); err != nil {
			return Streak{}, err
		}
		return st, nil
	}
	yesterday := today.AddDate(0, 0, -1)
	if st.LastCheckinDate != nil && utcDate(*st.LastCheckinDate).Equal(yesterday) {
		st.CurrentStreak++
	} else {
		st.CurrentStreak = 1
	}
	if st.CurrentStreak > st.LongestStreak {
		st.LongestStreak = st.CurrentStreak
	}
	st.LastCheckinDate = &today
	st.UpdatedAt = now.UTC()
	if err := store.UpsertStreak(ctx, st); err != nil {
		return Streak{}, err
	}
	return st, nil
}

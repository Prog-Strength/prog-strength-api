package user

import "time"

func validateBirthdate(birthdate string) error {
	bd, err := time.Parse("2006-01-02", birthdate)
	if err != nil {
		return ErrInvalidBirthdate
	}
	now := time.Now().UTC()
	if bd.After(now) {
		return ErrInvalidBirthdate
	}
	age := now.Year() - bd.Year()
	if now.Month() < bd.Month() || (now.Month() == bd.Month() && now.Day() < bd.Day()) {
		age--
	}
	if age < MinEstimateAge || age > MaxEstimateAge {
		return ErrInvalidBirthdate
	}
	return nil
}

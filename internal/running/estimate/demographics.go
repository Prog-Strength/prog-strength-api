package estimate

import "time"

// DemographicsFromProfile maps persisted profile fields into engine input.
// birthdate is ISO YYYY-MM-DD; age is computed at now and omitted when
// birthdate is missing or invalid.
func DemographicsFromProfile(heightCm *float64, birthdate, sex *string, now time.Time) Demographics {
	d := Demographics{
		HeightCm: heightCm,
		Sex:      sex,
	}
	if birthdate != nil {
		if age := ageAtBirthdate(*birthdate, now); age != nil {
			d.Age = age
		}
	}
	return d
}

func ageAtBirthdate(birthdate string, now time.Time) *int {
	bd, err := time.Parse("2006-01-02", birthdate)
	if err != nil {
		return nil
	}
	if bd.After(now) {
		return nil
	}
	age := now.Year() - bd.Year()
	if now.Month() < bd.Month() || (now.Month() == bd.Month() && now.Day() < bd.Day()) {
		age--
	}
	if age < 10 || age > 100 {
		return nil
	}
	return &age
}

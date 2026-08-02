package qweather

type commonResponse struct {
	Code       string `json:"code"`
	UpdateTime string `json:"updateTime"`
}

type currentResponse struct {
	commonResponse
	Now struct {
		ObservedAt    string `json:"obsTime"`
		Temperature   string `json:"temp"`
		FeelsLike     string `json:"feelsLike"`
		Icon          string `json:"icon"`
		Text          string `json:"text"`
		WindDegrees   string `json:"wind360"`
		WindDirection string `json:"windDir"`
		WindScale     string `json:"windScale"`
		WindSpeed     string `json:"windSpeed"`
		Humidity      string `json:"humidity"`
		Precipitation string `json:"precip"`
		Pressure      string `json:"pressure"`
		Visibility    string `json:"vis"`
		Cloud         string `json:"cloud"`
		DewPoint      string `json:"dew"`
	} `json:"now"`
}

type hourlyResponse struct {
	commonResponse
	Hourly []struct {
		ForecastAt          string `json:"fxTime"`
		Temperature         string `json:"temp"`
		Icon                string `json:"icon"`
		Text                string `json:"text"`
		WindDegrees         string `json:"wind360"`
		WindDirection       string `json:"windDir"`
		WindScale           string `json:"windScale"`
		WindSpeed           string `json:"windSpeed"`
		Humidity            string `json:"humidity"`
		PrecipitationChance string `json:"pop"`
		Precipitation       string `json:"precip"`
		Pressure            string `json:"pressure"`
		Cloud               string `json:"cloud"`
		DewPoint            string `json:"dew"`
	} `json:"hourly"`
}

type dailyResponse struct {
	commonResponse
	Daily []struct {
		Date               string `json:"fxDate"`
		Sunrise            string `json:"sunrise"`
		Sunset             string `json:"sunset"`
		Moonrise           string `json:"moonrise"`
		Moonset            string `json:"moonset"`
		MoonPhase          string `json:"moonPhase"`
		MoonPhaseCode      string `json:"moonPhaseIcon"`
		TemperatureMax     string `json:"tempMax"`
		TemperatureMin     string `json:"tempMin"`
		IconDay            string `json:"iconDay"`
		TextDay            string `json:"textDay"`
		IconNight          string `json:"iconNight"`
		TextNight          string `json:"textNight"`
		WindDayDegrees     string `json:"wind360Day"`
		WindDayDirection   string `json:"windDirDay"`
		WindDayScale       string `json:"windScaleDay"`
		WindDaySpeed       string `json:"windSpeedDay"`
		WindNightDegrees   string `json:"wind360Night"`
		WindNightDirection string `json:"windDirNight"`
		WindNightScale     string `json:"windScaleNight"`
		WindNightSpeed     string `json:"windSpeedNight"`
		Humidity           string `json:"humidity"`
		Precipitation      string `json:"precip"`
		Pressure           string `json:"pressure"`
		Visibility         string `json:"vis"`
		Cloud              string `json:"cloud"`
		UVIndex            string `json:"uvIndex"`
	} `json:"daily"`
}

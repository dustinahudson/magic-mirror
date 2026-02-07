#ifndef WEATHER_ICONS_H
#define WEATHER_ICONS_H

#include "lvgl.h"

#ifdef __cplusplus
extern "C" {
#endif

extern const lv_image_dsc_t weather_icon_clear_day;
extern const lv_image_dsc_t weather_icon_clear_day_small;
extern const lv_image_dsc_t weather_icon_clear_night;
extern const lv_image_dsc_t weather_icon_clear_night_small;
extern const lv_image_dsc_t weather_icon_partly_cloudy_day;
extern const lv_image_dsc_t weather_icon_partly_cloudy_day_small;
extern const lv_image_dsc_t weather_icon_partly_cloudy_night;
extern const lv_image_dsc_t weather_icon_partly_cloudy_night_small;
extern const lv_image_dsc_t weather_icon_cloudy;
extern const lv_image_dsc_t weather_icon_cloudy_small;
extern const lv_image_dsc_t weather_icon_fog;
extern const lv_image_dsc_t weather_icon_fog_small;
extern const lv_image_dsc_t weather_icon_drizzle;
extern const lv_image_dsc_t weather_icon_drizzle_small;
extern const lv_image_dsc_t weather_icon_rain;
extern const lv_image_dsc_t weather_icon_rain_small;
extern const lv_image_dsc_t weather_icon_snow;
extern const lv_image_dsc_t weather_icon_snow_small;
extern const lv_image_dsc_t weather_icon_thunderstorm;
extern const lv_image_dsc_t weather_icon_thunderstorm_small;
extern const lv_image_dsc_t icon_sunset;
extern const lv_image_dsc_t icon_wind_0;
extern const lv_image_dsc_t icon_wind_1;
extern const lv_image_dsc_t icon_wind_2;
extern const lv_image_dsc_t icon_wind_3;
extern const lv_image_dsc_t icon_wind_4;
extern const lv_image_dsc_t icon_wind_5;
extern const lv_image_dsc_t icon_wind_6;
extern const lv_image_dsc_t icon_wind_7;
extern const lv_image_dsc_t icon_wind_8;
extern const lv_image_dsc_t icon_wind_9;
extern const lv_image_dsc_t icon_wind_10;
extern const lv_image_dsc_t icon_wind_11;
extern const lv_image_dsc_t icon_wind_12;

const lv_image_dsc_t* get_weather_icon(int wmo_code, bool is_day, bool small_size);
const lv_image_dsc_t* get_wind_icon(int wind_speed_mph);

#ifdef __cplusplus
}
#endif

#endif

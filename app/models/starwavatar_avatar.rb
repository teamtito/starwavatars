class StarwavatarAvatar
  attr_accessor :md5

  def self.icons
    %w{
      akbar
      atat
      boba
      c3po
      chewie
      darth
      deathstar
      falcon
      leia
      lightsaber
      r2d2
      stormtrooper
      xwing
      yoda
      tie
    }
  end

  def initialize(md5)
    @md5 = md5
  end

  def letters
    ("a".."z").to_a
  end

  def numbers
    (0..9).to_a.map(&:to_s)
  end

  def numbers_and_letters
    @numbers_and_letters ||= numbers + letters
  end

  def unique_number(top_number, position)
    (top_number.to_f * ((Digest::MD5.hexdigest(md5).to_i(16)).to_f.to_s.reverse.gsub(/[^\d]/,'')[4 + position.to_i,4].to_f / 10000)).to_i
  end

  def icon
    self.class.icons[unique_number(14,1)]
  end

  def first_rgb
    unique_number(255, 2)
  end

  def second_rgb
    unique_number(255, 3)
  end

  def third_rgb
    unique_number(255, 4)
  end

  def color
    @color ||= Paleta::Color.new(:rgb, first_rgb, second_rgb, third_rgb)
  end

  def shaded_color
    palette[4]
  end

  def desaturated_color
    @complimentary_color ||= color.desaturate
  end

  def complementary_color
    @complimentary_color ||= color.complement
  end

  def inverted_color
    @inverted_color ||= color.invert
  end

  def background_color
    return shaded_color if color.similarity(shaded_color) > 0.1
    return desaturated_color if color.similarity(desaturated_color) > 0.1
    inverted_color
  end

  def palette
    @palette ||= Paleta::Palette.generate(:type => :shades, :from => :color, :size => 5, :color => color)
  end

  def svg
    Rails.application.assets.find_asset("star-wars-avatars/#{icon}.svg").
      pathname.read.gsub('#264A62', "##{color.hex}").
      gsub('#FFFFFF', "##{background_color.hex}")
  end

  def png
    list = Magick::Image.from_blob(svg) { self.format = 'SVG' }
    list.first.to_blob { self.format = 'PNG' }
  end
end